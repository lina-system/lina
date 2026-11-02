"""lina#12: renaming a profile moves its message text to the new agent (agent_id comes from the path)."""

import json
from pathlib import Path
from unittest.mock import patch

import pytest

from hermes_cli.profile_identity import migrate_profile_identity
from hermes_cli.profiles import create_profile, rename_profile
from hermes_state import SessionDB
from hermes_state_store import TEXT_CACHE
from tools.session_search_tool import session_search


@pytest.fixture()
def profile_env(tmp_path, monkeypatch):
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    default_home = tmp_path / ".hermes"
    default_home.mkdir(exist_ok=True)
    monkeypatch.setenv("HERMES_HOME", str(default_home))
    return default_home


def _seed(profile_dir: Path) -> None:
    db = SessionDB(profile_dir / "state.db")
    try:
        db.create_session("history", "cli")
        db.append_message("history", "user", content="where is the lighthouse logbook")
        db.append_message("history", "assistant", content="on the second shelf")
    finally:
        db.close()


def _agents(memory_store_sql) -> dict[str, list[str]]:
    rows = memory_store_sql("SELECT agent_id, uid FROM memory.messages ORDER BY uid")
    found: dict[str, list[str]] = {}
    for agent, uid in rows:
        found.setdefault(agent, []).append(uid)
    return found


def _rename(old: str, new: str) -> Path:
    with patch("hermes_cli.profiles.check_alias_collision", return_value="skip"):
        return rename_profile(old, new)


def test_a_renamed_profile_reads_its_history_and_the_old_agent_keeps_no_rows(profile_env, memory_store_sql):
    _seed(create_profile("harbor", no_alias=True))
    assert set(_agents(memory_store_sql)) == {"harbor"}

    new_dir = _rename("harbor", "lighthouse")

    TEXT_CACHE.clear()
    db = SessionDB(new_dir / "state.db")
    try:
        assert [m["content"] for m in db.get_messages_as_conversation("history")] == [
            "where is the lighthouse logbook", "on the second shelf"]
        found = json.loads(session_search(query="logbook", db=db))
        assert [r["session_id"] for r in found["results"]] == ["history"]
    finally:
        db.close()
    agents = _agents(memory_store_sql)
    assert set(agents) == {"lighthouse"}
    assert all(uid.startswith("agent:lighthouse:") for uid in agents["lighthouse"])


def test_running_the_rename_step_again_changes_nothing(profile_env, memory_store_sql):
    _seed(create_profile("harbor", no_alias=True))
    new_dir = _rename("harbor", "lighthouse")
    before = _agents(memory_store_sql)

    assert migrate_profile_identity("harbor", "lighthouse") is True

    assert _agents(memory_store_sql) == before
    TEXT_CACHE.clear()
    db = SessionDB(new_dir / "state.db")
    try:
        assert [m["content"] for m in db.get_messages_as_conversation("history")] == [
            "where is the lighthouse logbook", "on the second shelf"]
        assert db._conn.execute("SELECT count(*) FROM transcript_rows WHERE text_uid LIKE 'agent:harbor:%'"
                                ).fetchone()[0] == 0
    finally:
        db.close()


def test_an_interrupted_rename_is_repaired_by_running_the_step_again(profile_env, memory_store_sql):
    _seed(create_profile("harbor", no_alias=True))
    new_dir = _rename("harbor", "lighthouse")
    # As if the state.db rewrite never ran: the store moved, the transcript still names the old agent.
    db = SessionDB(new_dir / "state.db")
    try:
        db._conn.execute("UPDATE transcript_rows SET text_uid = replace(text_uid, 'agent:lighthouse:', 'agent:harbor:')")
        db._conn.commit()
    finally:
        db.close()

    assert migrate_profile_identity("harbor", "lighthouse") is True

    TEXT_CACHE.clear()
    db = SessionDB(new_dir / "state.db")
    try:
        assert [m["content"] for m in db.get_messages_as_conversation("history")] == [
            "where is the lighthouse logbook", "on the second shelf"]
    finally:
        db.close()


def test_rename_is_refused_while_the_store_is_unreachable(profile_env, monkeypatch, memory_store_sql):
    import hermes_state_store

    old_dir = create_profile("harbor", no_alias=True)
    _seed(old_dir)

    def unreachable():
        raise hermes_state_store.StoreUnavailableError("cannot reach the memory store: connection refused")

    monkeypatch.setattr(hermes_state_store, "resolve_owner_dsn", unreachable)
    with pytest.raises(hermes_state_store.StoreUnavailableError, match="cannot reach the memory store"):
        _rename("harbor", "lighthouse")

    assert old_dir.is_dir()
    assert not (old_dir.parent / "lighthouse").exists()
    assert set(_agents(memory_store_sql)) == {"harbor"}
