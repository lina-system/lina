"""``lina services``: one line per LINA service with its label, and the reserved OCX / worker entries (lina#31)."""
import argparse
import plistlib
import subprocess

import pytest

import hermes_cli.gateway as gateway_cli
from hermes_cli import lina_services
from hermes_cli.subcommands.services import build_services_parser


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


@pytest.mark.parametrize(("verb", "first", "second"), [("stop", "gateway", "store"), ("start", "store", "gateway")])
def test_all_stops_at_the_first_failure_and_leaves_the_rest(account, monkeypatch, capsys, verb, first, second):
    """A gateway that refused to stop (live sessions kept) keeps its store; a store that did not come up gets no
    gateway started on it."""
    calls = []

    def failing(v):
        calls.append(first)
        raise SystemExit(1)

    monkeypatch.setitem(lina_services._VERBS, first, failing)
    monkeypatch.setitem(lina_services._VERBS, second, lambda v: calls.append(second))
    assert lina_services.act(verb, "all") == 1
    assert calls == [first]
    assert second in capsys.readouterr().out


def test_all_goes_on_after_a_service_exits_0(account, monkeypatch):
    calls = []

    def ok_exit(v):
        calls.append("gateway")
        raise SystemExit(0)

    monkeypatch.setitem(lina_services._VERBS, "gateway", ok_exit)
    monkeypatch.setitem(lina_services._VERBS, "store", lambda v: calls.append("store"))
    assert lina_services.act("stop", "all") == 0
    assert calls == ["gateway", "store"]


@pytest.fixture
def recorded(monkeypatch):
    """Every service verb recorded as ``(service, verb)`` instead of run."""
    calls = []
    for name in lina_services.START_ORDER:
        monkeypatch.setitem(lina_services._VERBS, name, lambda verb, name=name: calls.append((name, verb)))
    return calls


@pytest.mark.parametrize("name", ["all", "store"])
def test_restart_takes_the_gateway_down_before_the_store_and_up_after_it(account, recorded, name):
    """No gateway runs on a store while that store restarts (restarting the store restarts what uses it)."""
    assert lina_services.act("restart", name) == 0
    assert recorded == [("gateway", "stop"), ("store", "stop"), ("store", "start"), ("gateway", "start")]


def test_restart_stops_before_starting_when_a_phase_fails(account, monkeypatch, recorded):
    def store_stop_fails(verb):
        recorded.append(("store", verb))
        if verb == "stop":
            raise SystemExit(1)

    monkeypatch.setitem(lina_services._VERBS, "store", store_stop_fails)
    assert lina_services.act("restart", "all") == 1
    assert recorded == [("gateway", "stop"), ("store", "stop")]


def test_the_store_is_not_stopped_alone_while_a_gateway_runs(account, monkeypatch, recorded, capsys):
    """A gateway of any profile on this root still uses the one store."""
    monkeypatch.setattr(gateway_cli, "find_gateway_pids", lambda **kw: [4242] if kw.get("all_profiles") else [])
    assert lina_services.act("stop", "store") == 1
    assert recorded == [] and "stop all" in capsys.readouterr().out
    monkeypatch.setattr(gateway_cli, "find_gateway_pids", lambda **kw: [])
    assert lina_services.act("stop", "store") == 0
    assert recorded == [("store", "stop")]


@pytest.mark.parametrize("verb", ["stop", "restart"])
def test_stopping_the_gateway_names_the_whole_host(account, verb):
    """From a profile the host serves, a plain ``gateway stop`` only parks that profile; the store is shared by every
    profile, so the services verbs stop the host gateway (``--all``)."""
    lina_services._gateway(verb)
    assert account["verbs"] == [{"gateway_command": verb, "all": True}]


def test_start_installs_the_missing_gateway_service_through_the_gateway_verbs(account):
    assert _run("start", "gateway") == 0
    assert account["verbs"] == [{"gateway_command": "install", "start_now": True, "start_on_login": True}]


def test_install_starts_the_gateway_only_with_services(account, capsys):
    assert lina_services.install_gateway_step(services=False) == 0
    assert account["verbs"] == [] and "lina services start gateway" in capsys.readouterr().out
    assert lina_services.install_gateway_step(services=True) == 0
    assert [verb["gateway_command"] for verb in account["verbs"]] == ["install"]


def test_service_definition_seam_runs_the_given_argv():
    plist = plistlib.loads(lina_services.service_definition(
        "launchd", "ai.lina.ocx", ["/opt/lina/bin/lina", "ocx", "run"], {"HERMES_HOME": "/srv/lina"}).encode())
    assert (plist["Label"], plist["ProgramArguments"], plist["EnvironmentVariables"]) == (
        "ai.lina.ocx", ["/opt/lina/bin/lina", "ocx", "run"], {"HERMES_HOME": "/srv/lina"})
    unit = lina_services.service_definition("systemd", "lina-worker", ["/opt/lina/bin/lina", "worker"], {})
    assert "ExecStart=/opt/lina/bin/lina worker" in unit.splitlines()
