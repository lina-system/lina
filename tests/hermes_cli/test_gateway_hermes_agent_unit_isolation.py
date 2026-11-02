"""LINA's gateway service never operates the Hermes Agent gateway on the same account.

LINA is a fork of Hermes Agent and an account can run both. Hermes Agent's gateway is the systemd unit
``hermes-gateway.service`` (launchd ``ai.hermes.gateway``, Windows ``Hermes_Gateway``) pinning
``HERMES_HOME=~/.hermes``. LINA's own service names differ, and a unit pinning a home that is not a
LINA home is never LINA's, even for the account's default LINA home.
"""
import os
from pathlib import Path

import pytest

import hermes_cli.gateway as gateway_cli
from hermes_cli import gateway_service_owner

HERMES_AGENT_NAMES = ("hermes-gateway", "ai.hermes.gateway", "Hermes_Gateway")


def _unit(home: Path) -> str:
    return f'[Service]\nEnvironment="HERMES_HOME={home}"\nExecStart=/usr/bin/hermes gateway run\n'


@pytest.fixture
def account(tmp_path, monkeypatch):
    """One Linux account: Hermes Agent's user unit pins ``~/.hermes``; LINA runs from ``~/.lina``."""
    home = tmp_path / "acct"
    lina, hermes = home / ".lina", home / ".hermes"
    for p in (lina, hermes):
        p.mkdir(parents=True)
    user_units, system_units = tmp_path / "user-units", tmp_path / "system-units"
    for d in (user_units, system_units):
        d.mkdir()
    hermes_unit = user_units / "hermes-gateway.service"
    hermes_unit.write_text(_unit(hermes), encoding="utf-8")
    hermes_system_unit = system_units / "hermes-gateway.service"
    hermes_system_unit.write_text(_unit(hermes), encoding="utf-8")

    import hermes_constants
    monkeypatch.setattr(hermes_constants, "_account_home", lambda: home)
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.delenv("LINA_HOME", raising=False)
    monkeypatch.setenv("HERMES_HOME", str(lina))
    monkeypatch.setattr(os, "geteuid", lambda: 1000)
    monkeypatch.setattr(gateway_cli, "user_systemd_unit_dir", lambda: user_units)
    monkeypatch.setattr(gateway_cli, "_SYSTEM_UNIT_DIR", system_units)
    monkeypatch.setattr(gateway_cli, "supports_systemd_services", lambda: True)
    # Never reach a real systemd or the account's linger/launcher state: systemctl is recorded.
    monkeypatch.setattr(gateway_cli, "_select_systemd_scope", lambda system=False: system)
    monkeypatch.setattr(gateway_cli, "_preflight_user_systemd", lambda: None)
    monkeypatch.setattr(gateway_cli, "_require_root_for_system_service", lambda action: None)
    monkeypatch.setattr(gateway_cli, "_systemctl_show", lambda props, system=False: {})
    monkeypatch.setattr(gateway_cli, "_mark_planned_stop", lambda *a, **k: None)
    monkeypatch.setattr(gateway_cli, "_prepare_service_launcher", lambda **k: None)
    monkeypatch.setattr(gateway_cli, "_ensure_linger_enabled", lambda: None)
    monkeypatch.setattr(gateway_cli, "_print_runtime_health", lambda: None)
    monkeypatch.setattr(gateway_cli, "get_systemd_linger_status", lambda: (True, ""))
    monkeypatch.setattr(gateway_cli, "generate_systemd_unit", lambda system=False, run_as_user=None: _unit(lina))
    # tmp_path is a temp dir; the temp-home write refusal is not what this file tests.
    monkeypatch.setattr(gateway_service_owner, "refuse_temp_home_service_write", lambda definition, kind: False)
    calls = []

    class _Done:
        returncode, stdout, stderr = 0, "", ""

    def _record(args, **kw):
        calls.append(list(args))
        return _Done()

    monkeypatch.setattr(gateway_cli, "_run_systemctl", _record)
    return {"lina": lina, "hermes": hermes, "user_units": user_units, "system_units": system_units,
            "hermes_units": (hermes_unit, hermes_system_unit), "calls": calls}


def _touched_hermes_agent(account) -> bool:
    named = any(any(name in arg for name in HERMES_AGENT_NAMES) for call in account["calls"] for arg in call)
    rewritten = any(path.read_text(encoding="utf-8") != _unit(account["hermes"]) for path in account["hermes_units"])
    adopted = Path(os.environ["HERMES_HOME"]) != account["lina"]
    return named or rewritten or adopted


@pytest.mark.platforms("linux")
def test_lina_service_names_are_not_hermes_agents(account, monkeypatch):
    assert gateway_cli.get_service_name() == "lina-gateway"
    assert gateway_cli.get_launchd_label() == "ai.lina.gateway"
    from hermes_cli import gateway_windows
    assert gateway_windows._TASK_NAME_DEFAULT == "LINA_Gateway"   # get_task_name() runs on Windows only


@pytest.mark.platforms("linux")
@pytest.mark.parametrize("system", [False, True])
def test_stop_leaves_the_hermes_agent_gateway_alone(account, system):
    try:
        gateway_cli.systemd_stop(system=system)
    except SystemExit:
        pass  # "not installed" is a fine answer; operating Hermes Agent's unit is not
    assert not _touched_hermes_agent(account)


@pytest.mark.platforms("linux")
@pytest.mark.parametrize("system", [False, True])
def test_status_does_not_report_the_hermes_agent_gateway(account, system, capsys):
    gateway_cli.systemd_status(system=system)
    assert not _touched_hermes_agent(account)
    assert "not installed" in capsys.readouterr().out


@pytest.mark.platforms("linux")
@pytest.mark.parametrize("system", [False, True])
def test_install_writes_lina_unit_beside_the_hermes_agent_unit(account, system):
    gateway_cli.systemd_install(system=system)
    assert not _touched_hermes_agent(account)
    units = account["system_units" if system else "user_units"]
    assert (units / "lina-gateway.service").read_text(encoding="utf-8") == _unit(account["lina"])
    assert account["calls"] == [["daemon-reload"], ["enable", "lina-gateway"]]


@pytest.mark.platforms("linux")
@pytest.mark.parametrize("system", [False, True])
def test_owner_guard_refuses_a_lina_named_unit_pinning_the_hermes_home(account, system):
    """The account-default exception (sudo remap) covers LINA homes only, never ``~/.hermes``."""
    gateway_cli.get_systemd_unit_path(system=system).write_text(_unit(account["hermes"]), encoding="utf-8")
    assert gateway_service_owner.foreign_systemd_unit_home(system) == str(account["hermes"])
    with pytest.raises(SystemExit) as exc:
        gateway_cli.systemd_stop(system=system)
    assert exc.value.code == 1
    assert account["calls"] == []
    assert Path(os.environ["HERMES_HOME"]) == account["lina"]
