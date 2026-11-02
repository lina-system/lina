"""``lina models upgrade`` / ``rollback`` (lina#32): the new version is started on a spare port with a copy of the
data dir and must answer ``GET /v1/models`` 200 before ``current`` moves; the data dir is backed up first, and
rollback restores it."""
import json

import pytest

from hermes_cli import lina_ocx, lina_ocx_service, lina_ocx_upgrade
from tests.hermes_cli._lina_ocx_fakes import install_fake_version, installed_root, other_ocx

NEW = "2.96.0"
NEW_INTEGRITY = "sha512-" + "C" * 86 + "=="


@pytest.fixture
def root(tmp_path, monkeypatch):
    root = installed_root(tmp_path / "lina")
    (root / "ocx" / "config.json").write_text('{"schema": 1}\n')
    monkeypatch.setattr(lina_ocx, "install_node", lambda root, pin, fetch=None: root / "runtime" / "node")
    installs = []

    def install_ocx(root, pin, node, version, integrity):
        installs.append((version, integrity))
        return install_fake_version(root, version)

    monkeypatch.setattr(lina_ocx, "install_ocx", install_ocx)
    services = []
    monkeypatch.setattr(lina_ocx_service, "stop", lambda root=None, out=print: services.append("stop") or 0)
    monkeypatch.setattr(lina_ocx_service, "start", lambda root=None, out=print: services.append("start") or 0)
    return {"root": root, "installs": installs, "services": services}


@pytest.mark.platforms("posix")
def test_a_failed_health_check_keeps_current_and_the_data(root, monkeypatch):
    monkeypatch.setattr(lina_ocx_upgrade, "TRIAL_WAIT_S", 3.0)
    (root["root"] / "ocx" / "fake-status").write_text("500")
    assert lina_ocx_upgrade.upgrade(root["root"], NEW, NEW_INTEGRITY) == 1
    assert lina_ocx.current_version(root["root"]) == "2.95.0"
    assert root["installs"] == [(NEW, NEW_INTEGRITY)]
    assert not lina_ocx.backup_dir(root["root"]).exists() and root["services"] == []
    assert not list((root["root"] / "vendor" / "ocx").glob(".trial-*"))


@pytest.mark.platforms("posix")
def test_a_healthy_version_is_switched_to_after_backing_up_the_data_and_rollback_restores_it(root, monkeypatch):
    monkeypatch.setattr(lina_ocx, "own_proxy_running", lambda root, port: True)
    assert lina_ocx_upgrade.upgrade(root["root"], NEW, NEW_INTEGRITY) == 0
    data, backup = lina_ocx.data_dir(root["root"]), lina_ocx.backup_dir(root["root"])
    assert lina_ocx.current_version(root["root"]) == NEW
    assert (backup / "config.json").read_text() == '{"schema": 1}\n'
    assert not (data / "argv").exists()  # the trial ran on a copy of the data dir, not on it
    assert root["services"] == ["stop", "start"]

    (data / "config.json").write_text('{"schema": 2}\n')  # what a forward migration by the new version leaves
    assert lina_ocx_upgrade.rollback(root["root"]) == 0
    assert lina_ocx.current_version(root["root"]) == "2.95.0"
    assert (data / "config.json").read_text() == '{"schema": 1}\n'
    assert lina_ocx_upgrade.rollback(root["root"]) == 1  # one upgrade, one rollback


@pytest.mark.platforms("posix")
def test_the_trial_copy_starts_on_its_spare_port_while_lina_s_ocx_runs_on_the_copied_one(root, monkeypatch):
    """The copied config.json names the service's port, where LINA's OCX answers; OCX 2.95.0 probes that port and
    would refuse the trial start, so the copy must carry the trial's spare port instead."""
    monkeypatch.setattr(lina_ocx, "own_proxy_running", lambda root, port: True)
    with other_ocx() as service_port:
        (root["root"] / "ocx" / "config.json").write_text(json.dumps({"port": service_port, "keep": "me"}))
        assert lina_ocx_upgrade.upgrade(root["root"], NEW, NEW_INTEGRITY) == 0
    assert lina_ocx.current_version(root["root"]) == NEW
    assert json.loads((root["root"] / "ocx" / "config.json").read_text()) == {"port": service_port, "keep": "me"}


def _record_services(root, monkeypatch, start_codes: list[int]) -> list[tuple]:
    """stop/start of the service, each with the ``current`` version and the data's config.json at that moment;
    *start_codes* are the start results in order."""
    config, events = lina_ocx.data_dir(root) / "config.json", []

    def stop(root=None, out=print):
        events.append(("stop", lina_ocx.current_version(root), config.read_text()))
        return 0

    def start(root=None, out=print):
        events.append(("start", lina_ocx.current_version(root), config.read_text()))
        if start_codes[0] != 0:
            config.write_text('{"schema": 2}\n')  # what the new version left before it failed
        return start_codes.pop(0)

    monkeypatch.setattr(lina_ocx_service, "stop", stop)
    monkeypatch.setattr(lina_ocx_service, "start", start)
    monkeypatch.setattr(lina_ocx, "own_proxy_running", lambda root, port: True)
    (root / "config.yaml").write_text(f"ocx:\n  port: {lina_ocx.free_port()}\n")
    install_fake_version(root, NEW)
    return events


def test_a_failed_backup_after_the_stop_starts_the_current_version_again(root, monkeypatch):
    events = _record_services(root["root"], monkeypatch, [0])

    def disk_full(*args, **kwargs):
        raise OSError(28, "No space left on device")

    monkeypatch.setattr(lina_ocx_upgrade.shutil, "copytree", disk_full)
    with pytest.raises(OSError):
        lina_ocx_upgrade._switch(root["root"], "2.95.0", NEW, out=lambda line: None)
    assert [(verb, version) for verb, version, _ in events] == [("stop", "2.95.0"), ("start", "2.95.0")]
    assert lina_ocx.current_version(root["root"]) == "2.95.0"
    assert lina_ocx_upgrade.rollback(root["root"], out=lambda line: None) == 1  # no partial backup to restore


def test_a_new_version_that_fails_as_a_service_is_stopped_before_its_data_is_restored(root, monkeypatch):
    events = _record_services(root["root"], monkeypatch, [1, 0])
    assert lina_ocx_upgrade._switch(root["root"], "2.95.0", NEW, out=lambda line: None) == 1
    assert events == [("stop", "2.95.0", '{"schema": 1}\n'), ("start", NEW, '{"schema": 1}\n'),
                      ("stop", NEW, '{"schema": 2}\n'), ("start", "2.95.0", '{"schema": 1}\n')]


def test_a_version_that_is_not_the_pin_needs_its_integrity(root, capsys):
    assert lina_ocx_upgrade.upgrade(root["root"], NEW, None) == 1
    assert "--integrity" in capsys.readouterr().out
    assert root["installs"] == [] and lina_ocx.current_version(root["root"]) == "2.95.0"
