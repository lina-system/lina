"""``lina worker serve`` as the LINA service ``worker`` (lina#35): launchd ``ai.lina.worker`` / systemd ``lina-worker``
written through ``service_definition``, registered on ``lina services``, and OCX not stopped under a running worker."""

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
    assert "Description=LINA lina-worker" in unit and "-m hermes_cli.main worker serve" in unit
    assert f'Environment="HERMES_HOME={tmp_path}"' in unit
    plist = plistlib.loads(service.definition("launchd", tmp_path).encode())
    assert plist["Label"] == "ai.lina.worker" and plist["ProgramArguments"][-2:] == ["worker", "serve"]


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


def test_the_worker_is_a_real_service_that_runs_on_ocx(monkeypatch, capsys):
    worker = lina_services.BY_NAME["worker"]
    assert not worker.issue and "worker" in lina_services._VERBS and "worker" in lina_services._STATE
    assert "worker" in lina_services.USED_BY["ocx"]
    calls = []
    monkeypatch.setitem(lina_services._VERBS, "ocx", lambda verb: calls.append(verb))
    monkeypatch.setitem(lina_services._RUNNING, "gateway", lambda: False)
    monkeypatch.setitem(lina_services._RUNNING, "worker", lambda: True)
    assert lina_services.act("stop", "ocx") == 1
    assert calls == [] and "lina services stop worker" in capsys.readouterr().out
