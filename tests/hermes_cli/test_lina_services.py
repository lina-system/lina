"""``lina services``: one line per LINA service with its label, and the reserved entries (lina#31)."""
import argparse
import contextlib
import os
import plistlib
import subprocess
import sys
from pathlib import Path

import psutil
import pytest

import hermes_cli.gateway as gateway_cli
from hermes_cli import lina_services
from hermes_cli.subcommands.services import build_services_parser
from tests.waiting import wait_until


@pytest.fixture
def account(tmp_path, monkeypatch):
    """An account whose LINA home is ``~/.lina``; nothing is installed, and the gateway verbs are recorded."""
    home = tmp_path / "acct"
    (home / ".lina").mkdir(parents=True)
    import hermes_constants
    monkeypatch.setattr(hermes_constants, "_account_home", lambda: home)
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.delenv("LINA_HOME", raising=False)
    monkeypatch.setenv("HERMES_HOME", str(home / ".lina"))
    hermes_constants._default_hermes_root_memo = None
    monkeypatch.setattr("hermes_cli.store.store_dir", lambda: tmp_path / "store")
    monkeypatch.setattr(gateway_cli, "_is_service_installed", lambda: False)
    # OCX's unit lives under the account's real config dir; keep `all` from seeing (and stopping) a real one.
    monkeypatch.setattr("hermes_cli.lina_ocx_service.definition_path", lambda kind: tmp_path / "units" / "lina-ocx")
    monkeypatch.setattr("hermes_cli.lina_web_service.definition_path", lambda kind: tmp_path / "units" / "lina-web")
    verbs = []
    monkeypatch.setattr(gateway_cli, "gateway_command", lambda args: verbs.append(vars(args)))
    return {"store": tmp_path / "store", "verbs": verbs}


def _run(*argv) -> int:
    parser = argparse.ArgumentParser()
    build_services_parser(parser.add_subparsers())
    args = parser.parse_args(["services", *argv])
    with pytest.raises(SystemExit) as exc:
        args.func(args)
    return exc.value.code


def _rows(capsys):
    assert _run() == 0
    return [line.split(None, 2) for line in capsys.readouterr().out.splitlines()]


@pytest.mark.parametrize("kind", ["systemd", "launchd"])
def test_every_registered_service_is_one_row_with_its_label_and_state(account, monkeypatch, capsys, kind):
    monkeypatch.setattr(gateway_cli, "_service_backend", lambda **kw: kind)
    rows = _rows(capsys)
    assert [row[0] for row in rows] == [service.name for service in lina_services.SERVICES]
    for (name, shown_label, shown_state), service in zip(rows, lina_services.SERVICES):
        assert shown_label == lina_services.label(service, kind)
        assert shown_state == lina_services.state(service)


@pytest.mark.parametrize(("kind", "prefix"), [("systemd", "lina-"), ("launchd", "ai.lina.")])
def test_service_labels_are_lina_names_never_hermes_agent_ones(account, kind, prefix):
    for service in lina_services.SERVICES:
        if service.name == "store":  # the Docker compose project, the same name on every backend
            continue
        assert lina_services.label(service, kind).startswith(prefix)
        assert "hermes" not in lina_services.label(service, kind).lower()


def test_a_reserved_service_shows_the_issue_that_adds_it(account):
    for service in lina_services.SERVICES:
        if service.issue:
            assert service.issue in lina_services.state(service)


def test_store_state_comes_from_store_status(account, monkeypatch):
    from hermes_cli.store import Store
    account["store"].mkdir()
    (account["store"] / "store.env").write_text("LINA_STORE_PORT=54320\n")
    up = {"db honcho-api honcho-deriver"}

    def run(argv, **kw):
        if "ps" in argv:
            return subprocess.CompletedProcess(argv, 0, next(iter(up)), "")
        return subprocess.CompletedProcess(argv, 0, "ctx, ltm, memory, vec", "")

    monkeypatch.setattr(lina_services, "_store", lambda: Store(account["store"], run=run, out=lambda line: None))
    assert lina_services.state(lina_services.BY_NAME["store"]) == "running"
    up.clear()
    up.add("db")
    assert lina_services.state(lina_services.BY_NAME["store"]) == "stopped"


@pytest.mark.parametrize("verb", ["start", "stop", "restart"])
def test_a_reserved_service_exits_1_naming_its_issue(account, monkeypatch, capsys, verb):
    reserved = lina_services.Service("later", "ai.lina.later", "lina-later", "LINA_Later", issue="#9999")
    monkeypatch.setitem(lina_services.BY_NAME, reserved.name, reserved)
    assert lina_services.act(verb, reserved.name) == 1
    assert "lina#9999" in capsys.readouterr().out
    assert account["verbs"] == []


@pytest.fixture
def recorded(monkeypatch):
    """Every service verb recorded as ``(service, verb)`` instead of run."""
    calls = []
    for name in list(lina_services._VERBS):
        monkeypatch.setitem(lina_services._VERBS, name, lambda verb, name=name: calls.append((name, verb)))
    return calls


@pytest.mark.parametrize("verb", ["stop", "restart"])
def test_a_service_is_not_stopped_while_a_service_that_uses_it_runs(account, monkeypatch, recorded, capsys, verb):
    """The gateway of any profile on this root uses the one memory store."""
    for used, users in lina_services.USED_BY.items():
        for user in users:
            monkeypatch.setitem(lina_services._RUNNING, user, lambda: True)
        assert lina_services.act(verb, used) == 1
        assert recorded == []
        assert f"lina services stop {users[0]}" in capsys.readouterr().out
        for user in users:
            monkeypatch.setitem(lina_services._RUNNING, user, lambda: False)
        assert lina_services.act(verb, used) == 0
        assert recorded == [(used, verb)]
        recorded.clear()


@contextlib.contextmanager
def _gateway_process(tmp_path, home):
    """A process the gateway scan takes for ``gateway run`` (a ``gateway/run.py``), running on *home*."""
    script = tmp_path / f"gw-{home.name}" / "gateway" / "run.py"
    script.parent.mkdir(parents=True)
    script.write_text("import time\ntime.sleep(120)\n")
    proc = subprocess.Popen([sys.executable, str(script)], env={**os.environ, "HERMES_HOME": str(home)})
    try:
        wait_until(lambda: str(script) in " ".join(psutil.Process(proc.pid).cmdline()), what="the gateway started")
        yield proc
    finally:
        proc.kill()
        proc.wait(timeout=10)


@pytest.mark.spawns_gateway_lookalike
@pytest.mark.platforms("linux")
def test_only_a_gateway_of_this_lina_root_holds_the_store(account, tmp_path):
    """The store and OCX refuse to stop while a gateway of THIS root runs; another home's gateway (a second root,
    a test's, Hermes Agent's beside LINA) on the same account does not count."""
    assert "gateway" in lina_services.USED_BY["store"]
    root = Path(os.environ["HERMES_HOME"])
    other = tmp_path / "other-home"
    other.mkdir()
    with _gateway_process(tmp_path, other):
        assert lina_services._RUNNING["gateway"]() is False
    profile = root / "profiles" / "ops"
    profile.mkdir(parents=True)
    with _gateway_process(tmp_path, profile):
        assert lina_services._RUNNING["gateway"]() is True


def test_the_worker_counts_only_in_its_own_lina_root(account, tmp_path):
    from hermes_cli.worker import serve
    serve.serve_once(tmp_path / "other-root" / "worker")
    assert lina_services._RUNNING["worker"]() is False
    serve.serve_once(Path(os.environ["HERMES_HOME"]) / "worker")
    assert lina_services._RUNNING["worker"]() is True


@pytest.mark.parametrize("verb", ["start", "stop", "restart"])
def test_the_gateway_verbs_pass_through_unchanged(account, monkeypatch, verb):
    """Host scope, external supervisors and the live-session question stay with the gateway command itself."""
    monkeypatch.setattr(gateway_cli, "_is_service_installed", lambda: True)
    assert lina_services.act(verb, "gateway") == 0
    assert account["verbs"] == [{"gateway_command": verb}]


def test_there_is_no_all_target(account):
    parser = argparse.ArgumentParser()
    build_services_parser(parser.add_subparsers())
    with pytest.raises(SystemExit):
        parser.parse_args(["services", "stop", "all"])


def test_ocx_is_used_by_the_gateway(account):
    """The gateway calls every model through OCX, so OCX is not stopped while a gateway runs."""
    assert "gateway" in lina_services.USED_BY["ocx"]


def test_start_installs_the_missing_gateway_service_through_the_gateway_verbs(account):
    assert _run("start", "gateway") == 0
    assert account["verbs"] == [{"gateway_command": "install", "start_now": True, "start_on_login": True}]


def test_install_starts_the_gateway_only_with_services(account, capsys):
    assert lina_services.install_gateway_step(services=False) == 0
    assert account["verbs"] == [] and "lina services start gateway" in capsys.readouterr().out
    assert lina_services.install_gateway_step(services=True) == 0
    assert [verb["gateway_command"] for verb in account["verbs"]] == ["install"]


def test_service_definition_seam_runs_the_given_argv(tmp_path):
    """launchd starts the argv under osascript with the service's own logs (lina#288); systemd runs it bare."""
    from hermes_cli.gateway_launchd import launchd_program_arguments
    logs = lina_services.service_logs(tmp_path, "ocx")
    plist = plistlib.loads(lina_services.service_definition(
        "launchd", "ai.lina.ocx", ["/opt/lina/bin/lina", "ocx", "run"], {"HERMES_HOME": "/srv/lina"},
        logs=logs).encode())
    assert (plist["Label"], plist["ProgramArguments"], plist["EnvironmentVariables"]) == (
        "ai.lina.ocx", launchd_program_arguments(["/opt/lina/bin/lina", "ocx", "run"], *logs),
        {"HERMES_HOME": "/srv/lina"})
    unit = lina_services.service_definition("systemd", "lina-worker", ["/opt/lina/bin/lina", "worker"], {},
                                            logs=logs)
    assert "ExecStart=/opt/lina/bin/lina worker" in unit.splitlines()
