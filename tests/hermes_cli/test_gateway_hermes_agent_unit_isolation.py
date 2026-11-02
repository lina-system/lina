"""LINA's gateway service never operates the Hermes Agent gateway on the same account.

LINA is a fork of Hermes Agent and an account can run both. Hermes Agent's gateway is the systemd unit
``hermes-gateway.service`` (launchd ``ai.hermes.gateway``, Windows ``Hermes_Gateway``) pinning
``HERMES_HOME=~/.hermes``. LINA's own service names differ, and a unit pinning a home that is not a
LINA home is never LINA's, even for the account's default LINA home.

The same holds for the serve/dashboard units, the one-off launchd runtime jobs and the pre-rename
leftovers (``hermes.service``, ``ai.hermes.gateway*``): restart, ``migrate-legacy`` and install leave
Hermes Agent's alone (#94).
"""
import fnmatch
import os
import plistlib
from pathlib import Path
from types import SimpleNamespace

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


def _legacy_unit(home: Path) -> str:
    """A pre-rename ``hermes.service`` gateway unit pinning *home*."""
    return f'[Service]\nEnvironment="HERMES_HOME={home}"\nExecStart=/usr/bin/hermes gateway run --replace\n'


def _named_in_calls(calls, name: str) -> bool:
    return any(name in arg for call in calls for arg in call)


@pytest.mark.platforms("linux")
@pytest.mark.parametrize("system", [False, True])
def test_install_keeps_the_hermes_agent_legacy_unit(account, system):
    """Non-interactive install removes legacy units; Hermes Agent's ``hermes.service`` is not LINA's."""
    legacy = account["user_units"] / "hermes.service"  # the legacy scan reads both scopes on every install
    legacy.write_text(_legacy_unit(account["hermes"]), encoding="utf-8")
    gateway_cli.systemd_install(system=system, non_interactive=True)
    assert legacy.read_text(encoding="utf-8") == _legacy_unit(account["hermes"])
    assert not _named_in_calls(account["calls"], "hermes.service")


@pytest.mark.platforms("linux")
def test_migrate_legacy_removes_only_lina_legacy_units(account, monkeypatch):
    monkeypatch.setattr(os, "geteuid", lambda: 0)
    monkeypatch.setattr(gateway_cli, "is_macos", lambda: False)
    hermes_legacy = account["user_units"] / "hermes.service"
    hermes_legacy.write_text(_legacy_unit(account["hermes"]), encoding="utf-8")
    lina_legacy = account["system_units"] / "hermes.service"
    lina_legacy.write_text(_legacy_unit(account["lina"]), encoding="utf-8")

    gateway_cli.gateway_command(SimpleNamespace(gateway_command="migrate-legacy", dry_run=False, yes=True))

    assert hermes_legacy.read_text(encoding="utf-8") == _legacy_unit(account["hermes"])
    assert not lina_legacy.exists()
    assert ["stop", "hermes.service"] in account["calls"]  # the LINA one, system scope
    assert account["calls"].count(["stop", "hermes.service"]) == 1


@pytest.mark.platforms("linux")
def test_migrate_legacy_keeps_an_unpinned_legacy_unit(account, monkeypatch):
    """A unit pinning no home cannot be proven LINA's, so it is left alone."""
    monkeypatch.setattr(gateway_cli, "is_macos", lambda: False)
    legacy = account["user_units"] / "hermes.service"
    legacy.write_text("[Service]\nExecStart=/usr/bin/hermes gateway run\n", encoding="utf-8")
    gateway_cli.gateway_command(SimpleNamespace(gateway_command="migrate-legacy", dry_run=False, yes=True))
    assert legacy.exists()
    assert account["calls"] == []


class _FakeSystemd:
    """systemctl over a fixed set of active units; every restart moves the unit to a new MainPID."""

    def __init__(self, units):
        self.pids = {unit: 100 + i for i, unit in enumerate(units)}
        self.restarted: list[str] = []

    def __call__(self, argv, **kwargs):
        verb = next(a for a in argv[1:] if not a.startswith("-"))
        unit = argv[argv.index(verb) + 1]
        out = {"list-units": self._list, "is-active": lambda _u: "active", "show": lambda u: str(self.pids[u]),
               "restart": self._restart}[verb](unit)
        return SimpleNamespace(returncode=0, stdout=out, stderr="")

    def _list(self, pattern):
        return "\n".join(f"{u} loaded active running x" for u in self.pids if fnmatch.fnmatch(u, pattern))

    def _restart(self, unit):
        self.restarted.append(unit)
        self.pids[unit] += 1000
        return ""


@pytest.mark.platforms("linux")
def test_serve_unit_restart_leaves_hermes_agent_units_alone(monkeypatch):
    from hermes_cli import update_restart_recovery as recovery

    systemd = _FakeSystemd(["hermes-serve.service", "hermes-dashboard-work.service",
                            "lina-serve.service", "lina-dashboard-work.service"])
    monkeypatch.setattr(recovery, "_systemctl_scopes", lambda: [("user", ["systemctl", "--user"])])

    result = recovery.restart_serve_units(run=systemd, sleep=lambda _s: None)

    assert sorted(systemd.restarted) == ["lina-dashboard-work.service", "lina-serve.service"]
    assert result == {"verified": ["user/lina-dashboard-work", "user/lina-serve"], "failed": []}


@pytest.mark.platforms("linux")
def test_fleet_restart_name_gate_excludes_hermes_agent_serve_units():
    from hermes_cli.update_cmd_fleet import _is_hermes_gateway_unit

    assert not any(map(_is_hermes_gateway_unit, ("hermes-serve.service", "hermes-dashboard.service",
                                                 "hermes-serve-work.service", "hermes-gateway.service")))
    assert all(map(_is_hermes_gateway_unit, ("lina-serve.service", "lina-dashboard-work.service",
                                             "lina-gateway.service")))


@pytest.mark.platforms("linux")
def test_dashboard_stop_does_not_restart_the_hermes_agent_dashboard_unit(monkeypatch):
    from hermes_cli import main_dashboard

    probes = []

    def _probe(cmd, *, timeout):
        probes.append(cmd)
        listed = "hermes-dashboard.service enabled enabled" if "list-unit-files" in cmd else ""
        state = "active" if "is-active" in cmd else "enabled" if "is-enabled" in cmd else listed
        return SimpleNamespace(returncode=0, stdout=state, stderr="")

    monkeypatch.setattr(main_dashboard, "_run_probe", _probe)
    assert main_dashboard._restart_managed_dashboard_service("update") is False
    assert not any("restart" in cmd for cmd in probes)


def _agent_plist(label: str, home: Path) -> bytes:
    return plistlib.dumps({"Label": label, "ProgramArguments": ["/usr/bin/lina", "gateway", "run"], "RunAtLoad": True,
                           "EnvironmentVariables": {"HERMES_HOME": str(home)}})


class _FakeLaunchd:
    """``launchctl`` over a set of loaded jobs: ``label -> (domain, plist path)``."""

    def __init__(self):
        self.loaded: dict[str, tuple[str, Path]] = {}
        self.calls: list[list[str]] = []

    def load(self, plist: Path, domain: str = f"gui/{os.getuid()}"):
        self.loaded[plistlib.loads(plist.read_bytes())["Label"]] = (domain, plist)

    def __call__(self, argv, **kwargs):
        self.calls.append(list(argv))
        if argv[:1] != ["launchctl"]:
            return SimpleNamespace(returncode=0, stdout="", stderr="")
        verb, args = argv[1], argv[2:]
        rc, out = {"list": self._list, "print": self._print, "bootout": self._bootout,
                   "bootstrap": self._bootstrap}.get(verb, lambda _a: (0, ""))(args)
        return SimpleNamespace(returncode=rc, stdout=out, stderr="")

    def _list(self, _args):
        return 0, "PID\tStatus\tLabel\n" + "".join(f"321\t0\t{label}\n" for label in self.loaded)

    def _print(self, args):
        domain, _, label = args[0].rpartition("/")
        job = self.loaded.get(label)
        return (0, f"{label} = {{\n\tpath = {job[1]}\n\tpid = 321\n}}\n") if job and job[0] == domain else (113, "")

    def _bootout(self, args):
        label = args[0].rpartition("/")[2]
        return (0, "") if self.loaded.pop(label, None) else (113, "")

    def _bootstrap(self, args):
        self.load(Path(args[1]), args[0])
        return 0, ""


@pytest.fixture
def launchd(account, tmp_path, monkeypatch):
    """A macOS account with LINA profile ``work``, an empty LaunchAgents folder and a fake launchd."""
    import subprocess

    (account["lina"] / "profiles" / "work").mkdir(parents=True)
    (account["lina"] / "profiles" / "work" / "config.yaml").write_text("{}\n", encoding="utf-8")
    agents = tmp_path / "LaunchAgents"
    agents.mkdir()
    fake = _FakeLaunchd()
    monkeypatch.setattr(subprocess, "run", fake)
    monkeypatch.setattr(gateway_cli, "get_launchd_plist_path", lambda: agents / "ai.lina.gateway.plist")
    return agents, fake


def _migrate_legacy():
    gateway_cli.gateway_command(SimpleNamespace(gateway_command="migrate-legacy", dry_run=False, yes=True))


@pytest.mark.platforms("macos")
def test_migrate_legacy_moves_only_lina_launch_agents(account, launchd):
    """LINA's pre-#93 ``ai.hermes.gateway*`` agents move to ``ai.lina.gateway*``; Hermes Agent's stay."""
    agents, fake = launchd
    hermes_agent = agents / "ai.hermes.gateway.plist"
    hermes_agent.write_bytes(_agent_plist("ai.hermes.gateway", account["hermes"]))
    fake.load(hermes_agent)
    lina_old = agents / "ai.hermes.gateway-work.plist"
    lina_old.write_bytes(_agent_plist("ai.hermes.gateway-work", account["lina"] / "profiles" / "work"))
    fake.load(lina_old)

    _migrate_legacy()

    assert hermes_agent.read_bytes() == _agent_plist("ai.hermes.gateway", account["hermes"])
    assert "ai.hermes.gateway" in fake.loaded
    assert not any(c[1] in ("bootout", "bootstrap") and c[-1].endswith(("/ai.hermes.gateway", "ai.hermes.gateway.plist"))
                   for c in fake.calls)
    assert not lina_old.exists() and "ai.hermes.gateway-work" not in fake.loaded
    moved = plistlib.loads((agents / "ai.lina.gateway-work.plist").read_bytes())
    assert moved["Label"] == "ai.lina.gateway-work"
    assert moved["EnvironmentVariables"]["HERMES_HOME"] == str(account["lina"] / "profiles" / "work")
    assert fake.loaded["ai.lina.gateway-work"][1] == agents / "ai.lina.gateway-work.plist"


@pytest.mark.platforms("macos")
def test_migrate_legacy_finds_a_loaded_agent_by_its_launchd_definition(account, launchd, tmp_path):
    """A loaded old LINA label is found through launchd and its loaded definition, not a folder glob:
    here the plist lives outside LaunchAgents and the label maps to no profile."""
    agents, fake = launchd
    elsewhere = tmp_path / "elsewhere"
    elsewhere.mkdir()
    old = elsewhere / "agent.plist"
    old.write_bytes(_agent_plist("ai.hermes.gateway-0a1b2c3d", account["lina"]))
    fake.load(old)

    _migrate_legacy()

    assert not old.exists() and "ai.hermes.gateway-0a1b2c3d" not in fake.loaded
    assert fake.loaded["ai.lina.gateway-0a1b2c3d"][1] == agents / "ai.lina.gateway-0a1b2c3d.plist"


@pytest.mark.platforms("macos")
def test_migrate_legacy_loads_an_existing_unloaded_lina_agent(account, launchd):
    """An ``ai.lina.gateway*`` plist that exists but is not loaded must not end the move with the gateway
    unsupervised: it is bootstrapped before the old definition goes."""
    agents, fake = launchd
    home = account["lina"] / "profiles" / "work"
    (agents / "ai.lina.gateway-work.plist").write_bytes(_agent_plist("ai.lina.gateway-work", home))
    old = agents / "ai.hermes.gateway-work.plist"
    old.write_bytes(_agent_plist("ai.hermes.gateway-work", home))
    fake.load(old)

    _migrate_legacy()

    assert "ai.lina.gateway-work" in fake.loaded
    assert not old.exists() and "ai.hermes.gateway-work" not in fake.loaded


def test_windows_service_owner_ignores_hermes_agent_service_names():
    from hermes_cli.gateway_windows import hermes_owns_windows_service

    assert not hermes_owns_windows_service("Hermes_Gateway", "", ())
    assert not hermes_owns_windows_service("HermesGateway", "C:/Users/u/AppData/Local/hermes/x.exe", ())
    assert hermes_owns_windows_service("LINA_Gateway", "", ())


@pytest.mark.platforms("posix")
def test_runtime_job_sweep_leaves_hermes_agent_jobs(monkeypatch):
    """The finished-job sweep removes only LINA's one-off runtime jobs, never Hermes Agent's."""
    from hermes_cli import gateway_launchd_runtime as runtime

    listing = ("PID\tStatus\tLabel\n-\t0\tcom.nousresearch.hermes.runtime.aaaaaaaaaaaa\n"
               f"-\t0\t{runtime.LABEL_PREFIX}bbbbbbbbbbbb\n")
    calls = []

    def _launchctl(*args, timeout):
        calls.append(args)
        return SimpleNamespace(returncode=0, stdout=listing if args[0] == "list" else "", stderr="")

    monkeypatch.setattr(runtime, "_launchctl", _launchctl)
    runtime._remove_finished_jobs(None)
    assert [args for args in calls if args[0] == "remove"] == [("remove", f"{runtime.LABEL_PREFIX}bbbbbbbbbbbb")]
    assert not runtime.LABEL_PREFIX.startswith(("com.nousresearch.hermes.", "ai.lina.", "ai.hermes."))
