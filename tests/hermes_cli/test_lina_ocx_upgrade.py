"""``lina models upgrade`` / ``rollback`` (lina#32): the new version is started on a spare port with a copy of the
data dir and must answer ``GET /v1/models`` 200 before ``current`` moves; the data dir is backed up first, and
rollback restores it."""
import pytest

from hermes_cli import lina_ocx, lina_ocx_service, lina_ocx_upgrade
from tests.hermes_cli._lina_ocx_fakes import install_fake_version, installed_root

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


def test_a_version_that_is_not_the_pin_needs_its_integrity(root, capsys):
    assert lina_ocx_upgrade.upgrade(root["root"], NEW, None) == 1
    assert "--integrity" in capsys.readouterr().out
    assert root["installs"] == [] and lina_ocx.current_version(root["root"]) == "2.95.0"
