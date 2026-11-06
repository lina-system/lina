"""The new agent's Honcho transport belongs only to the destination store."""

import json

from hermes_cli.migrate_memory import _publish_honcho_config


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
