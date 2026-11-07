"""File-node settings and per-agent access round trips (lina#389)."""

from __future__ import annotations

import json

import pytest

import hermes_yaml
from hermes_cli.lina_work_settings import SettingsError, set_path, validate
from hermes_cli.lina_work_settings_store import Store
from hermes_cli.worker import settings
from tests.hermes_cli.test_lina_work_settings_cli import _lina

CLI = {"kind": "cli", "principal": "tester", "device": "local"}
FILE = {"host": "files", "kind": "file", "role": "files", "omo": "~/.bun/bin/omo",
        "deny": ["/volume1/backup", "#recycle", "#snapshot", "@*"], "volumes": ["/volume1"],
        "root": "lina-file-node", "model_check": "http://127.0.0.1:10100/healthz", "scan_s": 300}
FILE_FIELDS = {key: value for key, value in FILE.items() if key not in ("host", "kind", "role")}


@pytest.fixture
def root(tmp_path, monkeypatch):
    base = tmp_path / "lina"
    base.mkdir()
    monkeypatch.setenv("LINA_HOME", str(base))
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / "hermes"))
    return base


@pytest.mark.parametrize(("key", "value"), [
    ("kind", "lina"), ("kind", "file"), *FILE_FIELDS.items(),
    ("deny", []), ("volumes", []), ("model_check", "https://models.example.test/health"), ("scan_s", 1),
])
def test_valid_file_fields_survive_store_and_reader(root, key, value):
    entry = {"host": "files", "kind": "file", key: value}
    rev, _ = Store(root).apply(lambda doc: set_path(doc, "devices.workers.files", entry), CLI)
    assert rev.doc["devices"]["workers"]["files"] == entry
    assert settings.devices(root) == settings.devices(raw={"files": entry})


@pytest.mark.parametrize(("key", "value"), [
    ("kind", "ssh"), ("kind", []), ("kind", None),
    ("omo", ""), ("omo", "  "), ("omo", 5),
    ("root", ""), ("root", "  "), ("root", {}),
    ("deny", "backup"), ("deny", [""]), ("deny", ["  "]), ("deny", [1]),
    ("volumes", "/volume1"), ("volumes", [""]), ("volumes", ["  "]), ("volumes", [None]),
    ("model_check", "ftp://node/health"), ("model_check", "http://"),
    ("model_check", "https:///health"), ("model_check", "http://node/has space"),
    ("model_check", "http://[broken"), ("model_check", 42),
    ("scan_s", 0), ("scan_s", -1), ("scan_s", True), ("scan_s", 1.5), ("scan_s", "30"),
])
def test_invalid_file_fields_are_refused_without_writing(root, key, value):
    store = Store(root)
    store.apply(lambda doc: set_path(doc, "devices.workers.files", {"host": "files"}), CLI)
    before = (root / "config.yaml").read_bytes()
    with pytest.raises(SettingsError) as refused:
        store.apply(lambda doc: set_path(doc, "devices.workers.files", {**FILE, key: value}), CLI)
    assert f"devices.workers.files.{key}" in [e["path"] for e in refused.value.errors]
    assert (root / "config.yaml").read_bytes() == before


@pytest.mark.parametrize("kind", [None, "lina"])
@pytest.mark.parametrize(("key", "value"), FILE_FIELDS.items())
def test_file_only_fields_are_refused_on_lina_devices(key, value, kind):
    entry = {"host": "files", key: value}
    if kind is not None:
        entry["kind"] = kind
    errors = validate({"devices": {"workers": {"files": entry}}})
    assert [e["path"] for e in errors] == [f"devices.workers.files.{key}"]


@pytest.mark.parametrize("role", ["work", "person"])
def test_file_node_refuses_conflicting_role(role):
    errors = validate({"devices": {"workers": {"files": {"host": "files", "kind": "file", "role": role}}}})
    assert "devices.workers.files.role" in [e["path"] for e in errors]


def test_file_node_omitted_fields_keep_runtime_defaults(root):
    entry = {"host": "files", "kind": "file"}
    Store(root).apply(lambda doc: set_path(doc, "devices.workers.files", entry), CLI)
    assert settings.devices(root) == [settings.Device("files", "files", kind="file", role="files")]


def test_person_accepts_explicit_lina_kind():
    assert validate({"devices": {"workers": {"phone": {
        "host": "phone", "kind": "lina", "role": "person"}}}}) == []


@pytest.mark.parametrize("value", [False, "full", [], {"bad.name": "full"}, {"": "full"},
                                  {"agent": "write"}, {"agent": False}, {"agent": []}, {"agent": None}])
def test_invalid_access_is_refused_without_writing(root, value):
    with pytest.raises(SettingsError) as refused:
        Store(root).apply(lambda doc: set_path(doc, "worker.access", value), CLI)
    assert any(e["path"].startswith("worker.access") for e in refused.value.errors)
    assert not (root / "config.yaml").exists()


def test_access_round_trip_rollback_and_outside_edit(root):
    access = {"files-agent": "full", "reader": "read-only", "monkey": "workspace-write", "env": "full"}
    store = Store(root)
    first, _ = store.apply(lambda doc: set_path(doc, "worker.access", access), CLI)
    assert first.doc["worker"]["access"] == access
    assert settings.section(root)["access"] == access
    assert store.revision(first.n).doc["worker"]["access"] == access
    store.apply(lambda doc: set_path(doc, "worker.access.files-agent", "read-only"), CLI)
    back, _ = store.rollback({**CLI, "kind": "rollback"})
    assert back.doc["worker"]["access"] == access
    assert settings.section(root)["access"] == access
    path = root / "config.yaml"
    path.write_text(path.read_text(encoding="utf-8").replace("files-agent: full", "files-agent: workspace-write"),
                    encoding="utf-8")
    outside = store.current()
    assert outside.doc["worker"]["access"]["files-agent"] == "workspace-write"
    assert store.history()[0]["by"]["kind"] == "outside"
    assert store.history()[0]["changes"] == [
        {"path": "worker.access.files-agent", "from": "full", "to": "workspace-write"}]
    restored, _ = store.rollback({**CLI, "kind": "rollback"})
    assert restored.doc["worker"]["access"] == access


def test_empty_access_mapping_removes_the_ceiling(root):
    store = Store(root)
    store.apply(lambda doc: set_path(doc, "worker.access", {"agent": "full"}), CLI)
    store.apply(lambda doc: set_path(doc, "worker.access", {}), CLI)
    assert settings.section(root).get("access", {}) == {}
    restored, _ = store.rollback({**CLI, "kind": "rollback"})
    assert restored.doc["worker"]["access"] == {"agent": "full"}


def test_documented_configuration_validates_and_cli_matches_direct_reader(root, tmp_path, capsys):
    config = {"devices": {"workers": {"files": FILE}}, "worker": {"access": {"files-agent": "full"}}}
    direct = tmp_path / "direct"
    direct.mkdir()
    path = direct / "config.yaml"
    path.write_text(hermes_yaml.safe_dump(config), encoding="utf-8")
    expected = settings.devices(direct)
    assert _lina("settings", "validate", str(path)) == 0
    assert _lina("settings", "set", "devices.workers.files", json.dumps(FILE)) == 0
    assert _lina("settings", "set", "worker.access", '{"files-agent": "full"}') == 0
    assert _lina("settings", "validate") == 0
    assert settings.devices(root) == expected
    assert settings.section(root)["access"] == {"files-agent": "full"}


def test_cli_file_node_is_listed_by_hosts(root, monkeypatch, capsys):
    from hermes_cli.worker import bidding

    def probe(candidates, worker_root, **kwargs):
        assert candidates == [settings.Device("files", "files", kind="file", role="files")]
        return [{"error": "offline test node"}]

    monkeypatch.setattr(bidding, "probe_all", probe)
    assert _lina("settings", "set", "devices.workers.files", '{"host": "files", "kind": "file"}') == 0
    capsys.readouterr()
    assert _lina("hosts") == 0
    rows = capsys.readouterr().out.splitlines()
    assert any(row.startswith("files ") and "cap 1" in row and "offline test node" in row for row in rows)
