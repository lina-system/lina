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


def test_lists_the_four_services_with_systemd_names(account, monkeypatch, capsys):
    monkeypatch.setattr(gateway_cli, "_service_backend", lambda **kw: "systemd")
    assert _rows(capsys) == [["gateway", "lina-gateway", "not installed"],
                             ["store", "lina-store", "not installed"],
                             ["ocx", "lina-ocx", "not installed yet (#32)"],
                             ["worker", "lina-worker", "not installed yet (#35)"]]


def test_lists_the_four_services_with_launchd_labels(account, monkeypatch, capsys):
    monkeypatch.setattr(gateway_cli, "_service_backend", lambda **kw: "launchd")
    assert _rows(capsys) == [["gateway", "ai.lina.gateway", "not installed"],
                             ["store", "lina-store", "not installed"],
                             ["ocx", "ai.lina.ocx", "not installed yet (#32)"],
                             ["worker", "ai.lina.worker", "not installed yet (#35)"]]


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


@pytest.mark.parametrize(("verb", "name", "issue"), [("start", "ocx", "#32"), ("stop", "ocx", "#32"),
                                                     ("start", "worker", "#35"), ("restart", "worker", "#35")])
def test_reserved_services_exit_1_naming_their_issue(account, capsys, verb, name, issue):
    assert _run(verb, name) == 1
    assert f"lina{issue}" in capsys.readouterr().out
    assert account["verbs"] == []


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
