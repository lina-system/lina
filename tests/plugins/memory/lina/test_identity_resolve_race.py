"""lina#190: two registries resolving unseen key sets that share an alias must end on one id.

The window is between the empty read and the insert. A lock on ``memory.identities`` holds both
transactions there, on two connections, so an instance-local lock cannot hide the split.
"""

from __future__ import annotations

import threading

from tests.waiting import wait_until


def test_concurrent_resolves_that_share_a_key_return_one_id(memory_store_sql):
    from tests._fixtures.memory_store import PROCESS_STORE, _connect
    from plugins.memory.lina.identity import IdentityRegistry

    memory_store_sql("SELECT 1")
    hold = _connect(PROCESS_STORE.superuser_dsn())
    hold.autocommit = False
    hold.execute("LOCK TABLE memory.identities IN SHARE ROW EXCLUSIVE MODE")
    left, right = IdentityRegistry(), IdentityRegistry()
    results: dict[str, str] = {}
    errors: dict[str, BaseException] = {}

    def resolve(name: str, registry: IdentityRegistry, keys: list[tuple[str, str]]) -> None:
        try:
            identity = registry.resolve("person", keys)
            if identity is not None:
                results[name] = identity
        except BaseException as exc:  # a failed resolve is the assertion, not a second failure mode
            errors[name] = exc
        finally:
            registry.close()

    threads = [
        threading.Thread(target=resolve, args=("a", left, [
            ("gateway_user", "user-a"), ("honcho_peer", "shared-alias")])),
        threading.Thread(target=resolve, args=("b", right, [
            ("gateway_user", "user-b"), ("honcho_peer", "shared-alias")])),
    ]
    try:
        for thread in threads:
            thread.start()
        wait_until(_both_blocked, timeout=15, what="both identity resolves to wait on the store")
    finally:
        hold.rollback()
        hold.close()
    for thread in threads:
        thread.join(30)
        assert not thread.is_alive()

    assert errors == {}
    assert results["a"] == results["b"]
    rows = memory_store_sql(
        "SELECT source, key, identity_id FROM memory.identity_keys WHERE kind = 'person' ORDER BY source, key")
    assert {(source, key) for source, key, _ in rows} == {
        ("gateway_user", "user-a"), ("gateway_user", "user-b"), ("honcho_peer", "shared-alias")}
    assert {identity for _, _, identity in rows} == {results["a"]}


def _both_blocked() -> bool:
    """Both resolves are inside the store, past the point where an empty read is cached in the caller."""
    from tests._fixtures.memory_store import PROCESS_STORE, _connect

    with _connect(PROCESS_STORE.superuser_dsn()) as conn:
        waiting = conn.execute(
            "SELECT count(*) FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND state = 'active' "
            "AND wait_event_type = 'Lock' AND (query LIKE '%memory.identit%' OR query LIKE '%pg_advisory_xact_lock%')"
        ).fetchone()
    return bool(waiting and waiting[0] >= 2)
