"""``lina worker serve`` as the LINA service ``worker`` (lina#35): launchd ``ai.lina.worker`` / systemd ``lina-worker``
written through ``service_definition``, registered on ``lina services``."""

import plistlib

import pytest

from hermes_cli import lina_services
from hermes_cli.worker import serve, service


@pytest.fixture
def unit_dirs(tmp_path, monkeypatch):
    """The service files land in temp dirs, never the developer's real LaunchAgents or systemd user dir."""
    monkeypatch.setattr(service, "definition_path", lambda kind: tmp_path / kind / f"{kind}.service")
    calls = []
    monkeypatch.setattr(service, "_run", lambda argv: calls.append(argv) or type("R", (), {"returncode": 0,
                                                                                         "stdout": "", "stderr": ""})())
    return calls


def test_the_definitions_run_lina_worker_serve_from_this_lina_home(tmp_path):
    unit = service.definition("systemd", tmp_path)
    assert "Description=LINA lina-worker" in unit and "-P -m hermes_cli.main worker serve" in unit
    assert f'Environment="HERMES_HOME={tmp_path}"' in unit
    plist = plistlib.loads(service.definition("launchd", tmp_path).encode())
    assert plist["Label"] == "ai.lina.worker" and plist["ProgramArguments"][0] == "/usr/bin/osascript"
    assert "-P -m hermes_cli.main worker serve >> " in plist["ProgramArguments"][-1]


@pytest.mark.platforms("linux")
def test_start_writes_the_unit_and_waits_for_the_heartbeat(tmp_path, monkeypatch, unit_dirs):
    base = tmp_path / "lina"
    monkeypatch.setattr(service, "running", lambda b=None: bool(unit_dirs))
    assert service.start(base, out=lambda line: None) == 0
    assert (tmp_path / "systemd" / "systemd.service").read_text() == service.definition("systemd", base)
    assert ["systemctl", "--user", "restart", "lina-worker"] in unit_dirs


def test_running_follows_the_serve_heartbeat(tmp_path):
    base = tmp_path / "lina"
    assert service.running(base) is False
    serve.serve_once(base / "worker")
    assert service.running(base) is True


def test_the_worker_is_a_registered_service():
    worker = lina_services.BY_NAME["worker"]
    assert not worker.issue and "worker" in lina_services._VERBS and "worker" in lina_services._STATE


@pytest.mark.platforms("linux")
def test_restart_starts_a_healthy_worker_again(tmp_path, monkeypatch):
    """A worker that wrote its heartbeat seconds ago is stopped and then really started again, not reported as
    "already running" from the stopped loop's last heartbeat."""
    base, calls = tmp_path / "lina", []

    def systemctl(argv):
        calls.append(argv)
        if argv[-2:] == ["restart", "lina-worker"]:
            serve.serve_once(base / "worker")  # the new loop's first heartbeat
        return type("R", (), {"returncode": 0, "stdout": "", "stderr": ""})()

    monkeypatch.setattr(service, "definition_path", lambda kind: tmp_path / kind / f"{kind}.service")
    monkeypatch.setattr(service, "_run", systemctl)
    monkeypatch.setattr(service, "START_WAIT_S", 2.0)
    (tmp_path / "systemd").mkdir()
    (tmp_path / "systemd" / "systemd.service").write_text("old unit")
    serve.serve_once(base / "worker")
    assert service.running(base)

    assert service.restart(base, out=lambda line: None) == 0
    assert ["systemctl", "--user", "stop", "lina-worker"] in calls
    assert calls.index(["systemctl", "--user", "restart", "lina-worker"]) > \
        calls.index(["systemctl", "--user", "stop", "lina-worker"])
