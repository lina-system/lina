"""LINA's services on macOS and the ``web`` service (lina#288).

* Every launchd job LINA writes starts under ``/usr/bin/osascript`` (``gateway_launchd.launchd_program_arguments``)
  with its own logs under the LINA root's ``logs/``; the systemd unit runs the same command bare.
* ``web`` (``ai.lina.web`` / ``lina-web``) runs the dashboard server of the LINA root on ``dashboard.host`` /
  ``dashboard.port``, never builds the UI, serves the configured app build at ``/app/``, and stays out of another
  home's dashboard on the same account: it neither attaches to that backend nor stops it.
* A service job never carries a Hermes card session's ``HERMES_KANBAN_*``, ``HERMES_DELEGATED_CHILD_CONTEXT`` or a
  ``HERMES_HOME`` of Hermes Agent's.

Plists and units are generated and read back; the one real server runs from a written unit on a scratch root and a
free port, never through the account's launchd or systemd.
"""

from __future__ import annotations

import argparse
import json
import os
import plistlib
import re
import shlex
import socket
import subprocess
import sys
import urllib.error
import urllib.request

import pytest

import hermes_constants
from hermes_cli import lina_services
from hermes_cli import lina_web_service as web
from hermes_cli.worker import service as worker_service
from hermes_cli.worker import settings as worker_settings
from tests.waiting import wait_until

APP_HTML = '<html><head></head><body><div id="root">lina-app</div></body></html>'


def _free_port() -> int:
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


def _systemd_parts(unit: str) -> tuple[list[str], dict[str, str]]:
    lines = unit.splitlines()
    env = dict(line.removeprefix('Environment="').rstrip('"').split("=", 1)
               for line in lines if line.startswith("Environment="))
    (cmd,) = [line.removeprefix("ExecStart=") for line in lines if line.startswith("ExecStart=")]
    return shlex.split(cmd), env


def _definitions(root, port):
    return {"worker": lambda kind: worker_service.definition(kind, root),
            "web": lambda kind: web.definition(kind, root)}


@pytest.mark.parametrize("name", ["worker", "web"])
def test_every_launchd_job_starts_under_osascript_with_its_own_logs(tmp_path, name):
    root = tmp_path / "lina"
    root.mkdir()
    define = _definitions(root, _free_port())[name]
    written = tmp_path / f"{name}.plist"
    written.write_text(define("launchd"), encoding="utf-8")
    plist = plistlib.loads(written.read_bytes())

    program = plist["ProgramArguments"]
    assert program[:4] == ["/usr/bin/osascript", "-l", "JavaScript", "-e"]
    shell = json.loads(re.search(r"\$\.system\((\".*?\")\);", program[4]).group(1))
    log, error_log = root / "logs" / f"{name}.log", root / "logs" / f"{name}.error.log"
    command, redirects = shell.removeprefix("exec ").split(" >> ", 1)
    assert redirects == f"{shlex.quote(str(log))} 2>> {shlex.quote(str(error_log))}"
    assert log.parent.is_dir()
    systemd_argv, _env = _systemd_parts(define("systemd"))
    assert shlex.split(command) == systemd_argv and "osascript" not in systemd_argv[0]
    if name in ("worker", "web"):
        assert systemd_argv[1:4] == ["-P", "-m", "hermes_cli.main"]
    assert (plist["RunAtLoad"], plist["KeepAlive"], plist["MaterializeDatalessFiles"]) == (True, True, True)


def _app_build(path):
    path.mkdir(parents=True)
    (path / "index.html").write_text(APP_HTML, encoding="utf-8")
    return path


def test_web_runs_the_dashboard_of_the_lina_root_on_the_configured_endpoint(tmp_path, monkeypatch):
    monkeypatch.delenv("LINA_APP_DIST", raising=False)
    root = tmp_path / "lina"
    root.mkdir()
    argv, env = _systemd_parts(web.definition("systemd", root))
    dashboard = argparse.ArgumentParser()
    from hermes_cli.main import cmd_dashboard
    from hermes_cli.subcommands.dashboard import build_dashboard_parser
    build_dashboard_parser(dashboard.add_subparsers(), cmd_dashboard=cmd_dashboard, cmd_dashboard_register=None)
    defaults = dashboard.parse_args(["dashboard"])
    assert argv[argv.index("--host") + 1] == defaults.host
    assert argv[argv.index("--port") + 1] == str(defaults.port)

    port = _free_port()
    (root / "config.yaml").write_text(f"dashboard:\n  host: localhost\n  port: {port}\n", encoding="utf-8")
    argv, env = _systemd_parts(web.definition("systemd", root))
    assert argv[:7] == [sys.executable, "-P", "-m", "hermes_cli.main", "-p", "default", "dashboard"]
    # No --skip-build and no HERMES_WEB_DIST: the install has no dashboard UI, and either makes the dashboard exit.
    assert argv[7:] == ["--host", "localhost", "--port", str(port), "--no-open", "--isolated"]
    assert env["HERMES_HOME"] == str(root) and "HERMES_WEB_DIST" not in env and "LINA_APP_DIST" not in env
    assert env["HERMES_GATEWAY_LOCK_DIR"] == str(web.lock_dir(root))

    monkeypatch.setenv("LINA_APP_DIST", str(tmp_path / "app"))  # the build start checks is the one the job serves
    assert _systemd_parts(web.definition("systemd", root))[1]["LINA_APP_DIST"] == str(tmp_path / "app")


def _active_profile(root, name="ops"):
    """A live named profile the person last picked with `profile use` (the sticky ``active_profile``)."""
    (root / "profiles" / name).mkdir(parents=True)
    (root / "profiles" / name / "config.yaml").write_text("{}\n", encoding="utf-8")
    (root / "active_profile").write_text(f"{name}\n", encoding="utf-8")


def test_the_service_command_runs_the_root_whatever_profile_is_active(tmp_path, monkeypatch):
    """The CLI's profile override reads the root's ``active_profile``; the service command must still land on the
    root, or the server opens a profile's state and ``running(root)`` never sees it."""
    from hermes_cli import main as hermes_main
    root = tmp_path / "lina"
    _active_profile(root)
    argv, env = _systemd_parts(web.definition("systemd", root))
    monkeypatch.setattr(hermes_constants, "_default_hermes_root_memo", None)
    monkeypatch.setenv("HERMES_HOME", env["HERMES_HOME"])
    monkeypatch.setattr(sys, "argv", ["hermes", *argv[4:]])
    hermes_main._apply_profile_override()
    assert os.environ["HERMES_HOME"] == str(root)
    assert sys.argv[1:3] == ["dashboard", "--host"]


def test_the_endpoint_comes_through_the_config_loader_and_a_bad_port_is_refused(tmp_path, monkeypatch):
    root = tmp_path / "lina"
    root.mkdir()
    port = _free_port()
    monkeypatch.setenv("LINA_WEB_TEST_PORT", str(port))
    (root / "config.yaml").write_text("dashboard:\n  port: ${LINA_WEB_TEST_PORT}\n", encoding="utf-8")
    argv, _env = _systemd_parts(web.definition("systemd", root))
    assert argv[argv.index("--port") + 1] == str(port)

    monkeypatch.setenv("LINA_APP_DIST", str(_app_build(tmp_path / "app")))
    (root / "config.yaml").write_text("dashboard:\n  port: nine\n", encoding="utf-8")
    calls, lines = [], []
    monkeypatch.setattr(web, "definition_path", lambda kind: tmp_path / "units" / "lina-web.service")
    monkeypatch.setattr(web, "_run", calls.append)
    assert web.start(root, out=lines.append) == 1
    assert len(lines) == 1 and "dashboard.port" in lines[0] and "is not a port number" in lines[0]
    assert "nine" not in lines[0]  # the configured value itself is never printed
    assert calls == [] and not (tmp_path / "units").exists()


@pytest.mark.platforms("linux")
def test_start_without_the_app_build_refuses_in_one_line_and_writes_nothing(tmp_path, monkeypatch):
    """No app build configured, a configured folder without ``index.html``, and a ``LINA_APP_DIST`` without one over
    a good ``dashboard.app_dist`` (the dashboard's order) all refuse, naming the keys and never a value."""
    root = tmp_path / "lina"
    root.mkdir()
    empty = tmp_path / "empty-build"
    empty.mkdir()
    app = _app_build(tmp_path / "app")
    calls = []
    monkeypatch.delenv("LINA_APP_DIST", raising=False)
    monkeypatch.setattr(web, "definition_path", lambda kind: tmp_path / "units" / "lina-web.service")
    monkeypatch.setattr(web, "_run", calls.append)
    cases = [("", None), (f"dashboard:\n  app_dist: {empty}\n", None), (f"dashboard:\n  app_dist: {app}\n", empty)]
    for config, env_dist in cases:
        (root / "config.yaml").write_text(config, encoding="utf-8")
        if env_dist:
            monkeypatch.setenv("LINA_APP_DIST", str(env_dist))
        lines = []
        assert web.start(root, out=lines.append) == 1, config
        assert len(lines) == 1 and "LINA_APP_DIST" in lines[0] and "dashboard.app_dist" in lines[0]
        assert str(tmp_path) not in lines[0] and "web_dist" not in lines[0]
    assert calls == [] and not (tmp_path / "units").exists()


@pytest.mark.platforms("linux")
def test_start_with_the_app_build_writes_the_unit_and_starts_it(tmp_path, monkeypatch):
    """No dashboard UI on the install (``hermes_cli/web_dist`` is never built on dev): the app build is enough."""
    root = tmp_path / "lina"
    root.mkdir()
    monkeypatch.delenv("LINA_APP_DIST", raising=False)
    (root / "config.yaml").write_text(f"dashboard:\n  app_dist: {_app_build(tmp_path / 'app')}\n", encoding="utf-8")
    unit, calls, lines = tmp_path / "units" / "lina-web.service", [], []
    monkeypatch.setattr(web, "definition_path", lambda kind: unit)
    monkeypatch.setattr(web, "_run", lambda argv: calls.append(argv) or subprocess.CompletedProcess(argv, 0, "", ""))
    monkeypatch.setattr(web, "_answers", lambda host, port: False)
    monkeypatch.setattr(web, "running", lambda root_: ["systemctl", "--user", "restart", "lina-web"] in calls)
    assert web.start(root, out=lines.append) == 0, lines
    assert unit.is_file() and lines[-1].startswith("✓ web running")


def test_service_jobs_never_carry_a_card_sessions_env(tmp_path, monkeypatch):
    """The shell a Hermes card session runs in: its kanban vars, its delegated-child marker, Hermes Agent's home."""
    monkeypatch.setattr(hermes_constants, "_account_home", lambda: tmp_path / "acct")
    monkeypatch.setattr(hermes_constants, "_default_hermes_root_memo", None)
    monkeypatch.delenv("LINA_HOME", raising=False)
    monkeypatch.setenv("HERMES_HOME", str(hermes_constants.hermes_agent_home()))
    monkeypatch.setenv("HERMES_KANBAN_TASK", "t_card")
    monkeypatch.setenv("HERMES_KANBAN_WORKSPACE", str(tmp_path / "card-workspace"))
    monkeypatch.setenv("HERMES_DELEGATED_CHILD_CONTEXT", "1")
    from hermes_cli.lina_deps import lina_root
    started_from = lina_root()  # what `lina services start` resolves in that shell
    definitions = {**_definitions(tmp_path / "lina", _free_port()),
                   "worker": lambda kind: worker_service.definition(kind, started_from),
                   "web": lambda kind: web.definition(kind, started_from)}
    for name, define in definitions.items():
        for env in (plistlib.loads(define("launchd").encode())["EnvironmentVariables"],
                    _systemd_parts(define("systemd"))[1]):
            assert not [key for key in env if key.startswith("HERMES_KANBAN_")], name
            assert "HERMES_DELEGATED_CHILD_CONTEXT" not in env, name
            assert not hermes_constants.is_hermes_agent_path(env.get("HERMES_HOME", "/nonexistent")), name


class _Systemd:
    """``systemctl --user`` for the test: ``restart lina-web`` runs the written unit's ExecStart with the account's
    environment under the unit's own ``Environment=`` lines, ``stop lina-web`` ends it."""

    def __init__(self, unit_path, account_env, log_path):
        self.unit_path, self.account_env, self.log_path = unit_path, account_env, log_path
        self.calls, self.proc = [], None

    def __call__(self, argv):
        self.calls.append(argv)
        if argv[-2:] == ["restart", "lina-web"]:
            argv_, env = _systemd_parts(self.unit_path.read_text())
            with open(self.log_path, "ab") as log:
                self.proc = subprocess.Popen(argv_, env={**self.account_env, **env}, stdout=log,
                                             stderr=subprocess.STDOUT)
        if argv[-2:] == ["stop", "lina-web"] and self.proc:
            self.proc.terminate()
            self.proc.wait(timeout=30)
        return subprocess.CompletedProcess(argv, 0, "", "")


def _answers(port: int) -> bool:
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=1):
            return True
    except OSError:
        return False


@pytest.mark.platforms("linux")
def test_web_serves_the_app_beside_another_homes_dashboard_and_stops_only_itself(tmp_path, monkeypatch):
    """Another home's dashboard (Hermes Agent's beside LINA) owns the account's one-backend record; the LINA web
    service still binds its own port and serves its app, leaves that record alone, and its stop ends only itself."""
    from gateway import host_rendezvous as hr
    dist = tmp_path / "other-web-dist"  # the other home's dashboard UI; the LINA service has none
    dist.mkdir()
    (dist / "index.html").write_text("<html><head></head><body>Dashboard</body></html>", encoding="utf-8")
    app = tmp_path / "lina-app"
    app.mkdir()
    (app / "index.html").write_text(APP_HTML, encoding="utf-8")
    root, other_home = tmp_path / "lina", tmp_path / "other-home"
    root.mkdir()
    other_home.mkdir()
    _active_profile(root)
    port, other_port = _free_port(), _free_port()
    (root / "config.yaml").write_text(f"dashboard:\n  port: {port}\n  app_dist: {app}\n", encoding="utf-8")
    account_env = {**os.environ, "HOME": str(tmp_path / "acct"), "PYTHONPATH": worker_settings.child_pythonpath()}
    account_env.pop("LINA_APP_DIST", None)

    other = subprocess.Popen(
        [sys.executable, "-m", "hermes_cli.main", "dashboard", "--host", "127.0.0.1", "--port", str(other_port),
         "--no-open", "--skip-build"],
        env={**account_env, "HERMES_HOME": str(other_home), "HERMES_WEB_DIST": str(dist)},
        stdout=open(tmp_path / "other.log", "ab"), stderr=subprocess.STDOUT)
    systemd = _Systemd(tmp_path / "units" / "lina-web.service", account_env, tmp_path / "web.log")
    monkeypatch.delenv("LINA_APP_DIST", raising=False)
    monkeypatch.setattr(web, "definition_path", lambda kind: systemd.unit_path)
    monkeypatch.setattr(web, "_run", systemd)
    try:
        record = wait_until(lambda: (r := hr.read_record(hr.ROLE_SERVE)) and r.pid == other.pid and r,
                            timeout=90, what="the other home's dashboard published the host record")
        assert web.state(root) == "not installed"

        assert web.start(root, out=lambda line: None) == 0, (tmp_path / "web.log").read_text()
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/app/", timeout=10) as page:
            assert page.status == 200 and '<div id="root">lina-app</div>' in page.read().decode()
        with pytest.raises(urllib.error.HTTPError) as no_ui:  # no dashboard UI: `/` is a 404 per request, no crash
            urllib.request.urlopen(f"http://127.0.0.1:{port}/", timeout=10)
        assert no_ui.value.code == 404
        assert web.state(root) == "running"
        assert web.own_pids(root) == [systemd.proc.pid]
        assert hr.read_record(hr.ROLE_SERVE).pid == record.pid  # LINA's server never became the account's owner

        assert web.stop(root, out=lambda line: None) == 0
        assert systemd.proc.poll() is not None and not _answers(port)
        assert other.poll() is None and _answers(other_port)
        assert web.state(root) == "stopped"
    finally:
        for proc in (systemd.proc, other):
            if proc and proc.poll() is None:
                proc.terminate()
                proc.wait(timeout=30)


def test_web_is_a_listed_service_with_its_own_verbs():
    service = lina_services.BY_NAME["web"]
    assert (service.launchd, service.systemd) == (web.LAUNCHD_LABEL, web.SYSTEMD_UNIT) == ("ai.lina.web", "lina-web")
    assert lina_services._VERBS["web"] and lina_services._STATE["web"]
    assert set(web.VERBS) == {"start", "stop", "restart"}
