"""The new agent's Honcho transport belongs only to the destination store."""

import json
from unittest.mock import MagicMock

import pytest

from hermes_cli.migrate_memory import _publish_honcho_config
from hermes_cli.migrate_memory_copy import snapshot
from tests.hermes_cli.test_migrate_memory_copy import database


def _copy_honcho(tmp_path, host_block, owner_ids=()):
    """Snapshot a source profile whose honcho.json holds *host_block*, publish the copy's honcho.json, return it."""
    source = tmp_path / "source/profiles/alpha"
    database(source / "state.db")
    (source / "honcho.json").write_text(json.dumps({"hosts": {"hermes_alpha": {
        "workspace": "shared", "aiPeer": "old-ai", "peerName": "person", **host_block}}}))
    copied = snapshot(tmp_path / "source", "alpha", tmp_path / "work")
    destination = tmp_path / "lina/profiles/alpha-copy"
    destination.mkdir(parents=True)
    _publish_honcho_config(copied.root / "external/honcho-config.json", destination, "alpha", "alpha-copy",
                           tmp_path / "lina/store", owner_ids)
    return destination / "honcho.json"


def _memory_files_migrate(config_path, runtime_id, tmp_path, monkeypatch) -> bool:
    """Whether the copy's Honcho, loaded from *config_path*, uploads the owner's memory files for *runtime_id*."""
    from plugins.memory.honcho import session as session_module
    from plugins.memory.honcho.client import HonchoClientConfig
    from plugins.memory.honcho.session import HonchoSession, HonchoSessionManager

    client = MagicMock()
    monkeypatch.setattr(session_module, "get_honcho_client", lambda *a, **k: client)
    config = HonchoClientConfig.from_global_config(host="hermes_alpha-copy", config_path=config_path)
    manager = HonchoSessionManager(honcho=client, config=config, runtime_user_peer_name=runtime_id)
    try:
        session = HonchoSession(key="telegram:dm", user_peer_id=manager._resolve_user_peer_id("telegram:dm"),
                                assistant_peer_id="alpha-copy", honcho_session_id="telegram-dm", messages=[])
        manager._cache[session.key] = session
        manager._sessions_cache[session.honcho_session_id] = MagicMock()
        memories = tmp_path / "memories"
        memories.mkdir(exist_ok=True)
        (memories / "USER.md").write_text("owner profile\n", encoding="utf-8")
        return manager.migrate_memory_files(session.key, str(memories))
    finally:
        manager.shutdown()


def test_copy_recognises_the_owner_the_source_declared(tmp_path, monkeypatch):
    # Given a source whose honcho.json aliases the owner's messenger id to peerName and a friend's to the friend.
    config = _copy_honcho(tmp_path, {"userPeerAliases": {"7000001": "person", "7000002": "friend"}})

    # Then the copy imports the owner's memory files for the owner only.
    assert _memory_files_migrate(config, "7000001", tmp_path, monkeypatch) is True
    assert _memory_files_migrate(config, "7000002", tmp_path, monkeypatch) is False
    assert _memory_files_migrate(config, "7000003", tmp_path, monkeypatch) is False


def test_owner_ids_given_to_migrate_become_owner_aliases(tmp_path, monkeypatch):
    # Given a source that declares no owner alias, migrated with the owner's messenger and CLI ids.
    config = _copy_honcho(tmp_path, {}, owner_ids=["7000001", "uid-1000"])

    assert json.loads(config.read_text())["hosts"]["hermes_alpha-copy"]["userPeerAliases"] == {
        "7000001": "person", "uid-1000": "person"}
    assert _memory_files_migrate(config, "7000001", tmp_path, monkeypatch) is True
    assert _memory_files_migrate(config, "uid-1000", tmp_path, monkeypatch) is True
    assert _memory_files_migrate(config, "7000003", tmp_path, monkeypatch) is False


def test_copy_without_owner_declaration_still_skips_messenger_users(tmp_path, monkeypatch):
    config = _copy_honcho(tmp_path, {})

    assert "userPeerAliases" not in json.loads(config.read_text())["hosts"]["hermes_alpha-copy"]
    assert _memory_files_migrate(config, "7000001", tmp_path, monkeypatch) is False


def test_owner_id_aliased_to_another_peer_is_refused(tmp_path):
    with pytest.raises(ValueError, match="another peer"):
        _copy_honcho(tmp_path, {"userPeerAliases": {"7000002": "friend"}}, owner_ids=["7000002"])


def test_destination_honcho_config_never_copies_source_transport(tmp_path):
    selection = tmp_path / "selection.json"
    selection.write_text(json.dumps({"hosts": {"hermes_alpha": {
        "workspace": "shared", "aiPeer": "old-ai", "peerName": "person", "observationMode": "unified"}}}))
    store = tmp_path / "lina/store"
    store.mkdir(parents=True)
    (store / "store.env").write_text("LINA_HONCHO_PORT=8123\n")
    destination = tmp_path / "lina/profiles/alpha-copy"
    destination.mkdir(parents=True)

    _publish_honcho_config(selection, destination, "alpha", "alpha-copy", store)

    config = json.loads((destination / "honcho.json").read_text())
    assert config == {"hosts": {"hermes_alpha-copy": {
        "workspace": "alpha-copy", "aiPeer": "alpha-copy", "peerName": "person",
        "observationMode": "unified", "baseUrl": "http://127.0.0.1:8123", "enabled": True}}}
    assert all("old-ai" not in path.read_text() for path in destination.rglob("*") if path.is_file())


def test_destination_honcho_refuses_changed_configuration(tmp_path):
    import pytest

    selection = tmp_path / "selection.json"
    selection.write_text(json.dumps({"hosts": {"hermes": {
        "workspace": "shared", "aiPeer": "source-ai", "peerName": "person", "observationMode": "directional"}}}))
    destination = tmp_path / "profiles/copy"
    destination.mkdir(parents=True)
    target = destination / "honcho.json"
    target.write_text('{"hosts":{"another":{"workspace":"unrelated"}}}')
    before = target.read_bytes()

    with pytest.raises(ValueError, match="configuration differs"):
        _publish_honcho_config(selection, destination, "default", "copy", tmp_path / "store")

    assert target.read_bytes() == before
