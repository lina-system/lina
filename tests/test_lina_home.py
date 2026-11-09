"""LINA keeps its data in its own home (lina#30).

``LINA_HOME`` names the root of every path the core uses (default ``~/.lina``), and an inherited
``HERMES_HOME`` that points into Hermes Agent's home, or outside an explicit ``LINA_HOME``, is dropped
at bootstrap so LINA never opens the Hermes install it was forked from.
"""
import json
import os
import subprocess
import sys
from pathlib import Path

import pytest

import hermes_constants
from hermes_cli.install_state import Places, candidate_roots
from hermes_constants import (
    drop_foreign_hermes_home,
    get_default_hermes_root,
    get_hermes_home,
    platform_default_home_for,
)
from tools.approval_detection import detect_dangerous_command

ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture
def account(tmp_path, monkeypatch):
    """An OS account whose Hermes Agent home is ``<account>/.hermes``."""
    home = tmp_path / "account"
    (home / ".hermes").mkdir(parents=True)
    monkeypatch.setattr(hermes_constants, "_account_home", lambda: home)
    monkeypatch.setattr(Path, "home", lambda: home)
    monkeypatch.delenv("LINA_HOME", raising=False)
    monkeypatch.delenv("HERMES_HOME", raising=False)
    return home


@pytest.mark.platforms("posix")
def test_default_home_is_dot_lina_and_lina_home_overrides_it(tmp_path):
    assert platform_default_home_for({}, tmp_path) == tmp_path / ".lina"
    assert platform_default_home_for({"LINA_HOME": str(tmp_path / "data")}, tmp_path) == tmp_path / "data"


@pytest.mark.platforms("posix")
def test_lina_home_maps_every_core_path(account, tmp_path, monkeypatch):
    root = tmp_path / "lina-data"
    monkeypatch.setenv("LINA_HOME", str(root))

    assert get_hermes_home() == root
    assert get_default_hermes_root() == root
    assert hermes_constants.get_config_path().parent == root
    assert hermes_constants.get_env_path().parent == root
    assert hermes_constants.get_skills_dir().parent == root
    profile = root / "profiles" / "work"
    profile.mkdir(parents=True)
    monkeypatch.setenv("HERMES_HOME", str(profile))
    assert get_default_hermes_root() == root


@pytest.mark.platforms("posix")
def test_inherited_hermes_home_into_hermes_agent_is_dropped(account):
    env = {"HERMES_HOME": str(account / ".hermes" / "profiles" / "coder")}
    assert drop_foreign_hermes_home(env) == str(account / ".hermes" / "profiles" / "coder")
    assert "HERMES_HOME" not in env


@pytest.mark.platforms("posix")
def test_hermes_home_outside_explicit_lina_home_is_dropped(account, tmp_path):
    env = {"LINA_HOME": str(tmp_path / "lina"), "HERMES_HOME": str(tmp_path / "elsewhere")}
    assert drop_foreign_hermes_home(env) == str(tmp_path / "elsewhere")
    assert "HERMES_HOME" not in env


@pytest.mark.platforms("posix")
def test_profile_handoff_and_custom_homes_survive(account, tmp_path):
    inside = {"LINA_HOME": str(tmp_path / "lina"), "HERMES_HOME": str(tmp_path / "lina" / "profiles" / "w")}
    default_profile = {"HERMES_HOME": str(account / ".lina" / "profiles" / "w")}
    container = {"HERMES_HOME": "/opt/data"}
    for env in (inside, default_profile, container):
        before = dict(env)
        assert drop_foreign_hermes_home(env) is None
        assert env == before


@pytest.mark.platforms("posix")
def test_a_dropped_hermes_agent_home_is_kept_quietly_as_the_callers(account, monkeypatch, capsys):
    import hermes_bootstrap
    profile = str(account / ".hermes" / "profiles" / "agent-b")
    monkeypatch.setenv("HERMES_HOME", profile)
    monkeypatch.setenv("LINA_CALLER_HERMES_HOME", "")

    hermes_bootstrap._drop_foreign_home()

    assert "HERMES_HOME" not in os.environ
    assert os.environ["LINA_CALLER_HERMES_HOME"] == profile
    assert capsys.readouterr().err == ""


@pytest.mark.platforms("posix")
def test_a_caller_home_already_kept_is_not_overwritten(account, monkeypatch, capsys):
    import hermes_bootstrap
    monkeypatch.setenv("HERMES_HOME", str(account / ".hermes" / "profiles" / "agent-b"))
    monkeypatch.setenv("LINA_CALLER_HERMES_HOME", str(account / ".hermes" / "profiles" / "agent-a"))

    hermes_bootstrap._drop_foreign_home()

    assert "HERMES_HOME" not in os.environ
    assert os.environ["LINA_CALLER_HERMES_HOME"] == str(account / ".hermes" / "profiles" / "agent-a")
    assert capsys.readouterr().err == ""


@pytest.mark.platforms("posix")
def test_a_dropped_home_that_is_not_hermes_agents_is_reported(account, tmp_path, monkeypatch, capsys):
    import hermes_bootstrap
    monkeypatch.setenv("LINA_HOME", str(tmp_path / "lina"))
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / "elsewhere"))
    monkeypatch.setenv("LINA_CALLER_HERMES_HOME", "")

    hermes_bootstrap._drop_foreign_home()

    assert "HERMES_HOME" not in os.environ
    assert os.environ["LINA_CALLER_HERMES_HOME"] == ""
    assert f"lina: ignoring HERMES_HOME={tmp_path / 'elsewhere'}" in capsys.readouterr().err


@pytest.mark.platforms("posix")
def test_install_discovery_skips_hermes_agent_home(account):
    lina = account / ".lina"
    lina.mkdir()
    places = Places(markers=(), sources=(lambda: [account / ".hermes", lina],))
    assert list(candidate_roots(places)) == [lina.resolve()]


@pytest.mark.platforms("posix")
def test_terminal_writes_to_the_lina_config_need_approval(account, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(account / ".lina"))
    for command in (
        "echo 'approvals:' > ~/.lina/config.yaml",
        "echo x | tee $LINA_HOME/.env",
        f"cp /tmp/evil.yaml {account}/.lina/config.yaml",
    ):
        dangerous, key, _desc = detect_dangerous_command(command)
        assert dangerous is True, command
        assert key is not None, command


@pytest.mark.platforms("posix")
@pytest.mark.parametrize(("inherited", "lina_home", "expected"), [
    (None, None, ".lina"),
    (".hermes/profiles/x", None, ".lina"),
    ("custom", None, "custom"),
    ("custom", "data", "data"),
    ("data/profiles/x", "data", "data/profiles/x"),
])
def test_installer_home_follows_the_runtime_rule(tmp_path, inherited, lina_home, expected):
    env = {k: v for k, v in os.environ.items() if k not in {"HERMES_HOME", "LINA_HOME"}}
    env["HOME"] = str(tmp_path)
    if inherited:
        env["HERMES_HOME"] = str(tmp_path / inherited)
    if lina_home:
        env["LINA_HOME"] = str(tmp_path / lina_home)
    script = f'source "{ROOT / "scripts" / "install.sh"}" --manifest >/dev/null; printf "%s" "$HERMES_HOME"'
    result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True,
                            encoding="utf-8", timeout=60, check=False)
    assert result.returncode == 0, result.stderr
    assert Path(result.stdout) == tmp_path / expected


def _lina(args: list[str], home: Path, **extra: str) -> subprocess.CompletedProcess:
    env = {k: v for k, v in os.environ.items() if k not in {"HERMES_HOME", "LINA_HOME"}}
    env.update(HOME=str(home), **extra)
    return subprocess.run([sys.executable, str(ROOT / "lina"), *args], cwd=ROOT, env=env, capture_output=True,
                          text=True, encoding="utf-8", timeout=120, check=False)


def _tree(root: Path) -> dict[str, tuple[int, int]]:
    return {str(p.relative_to(root)): (p.stat().st_mtime_ns, p.stat().st_size) for p in root.rglob("*")}


@pytest.mark.platforms("posix")
def test_lina_cli_resolves_its_own_home_and_leaves_hermes_alone(tmp_path):
    user = tmp_path / "user"
    hermes = user / ".hermes"
    (hermes / "profiles" / "coder").mkdir(parents=True)
    (hermes / "config.yaml").write_text("model: hermes-only\n", encoding="utf-8")
    before = _tree(hermes)

    default = _lina(["config", "path"], user)
    custom = _lina(["config", "path"], user, LINA_HOME=str(tmp_path / "data"))

    assert default.returncode == 0, default.stderr
    assert Path(default.stdout.strip()) == user / ".lina" / "config.yaml"
    assert custom.returncode == 0, custom.stderr
    assert Path(custom.stdout.strip()) == tmp_path / "data" / "config.yaml"
    assert _tree(hermes) == before


@pytest.mark.platforms("posix")
def test_lina_cli_ignores_a_foreign_hermes_home(tmp_path):
    foreign = tmp_path / "user" / ".hermes"
    foreign.mkdir(parents=True)
    result = _lina(["config", "path"], tmp_path / "user", LINA_HOME=str(tmp_path / "data"),
                   HERMES_HOME=str(foreign))

    assert result.returncode == 0, result.stderr
    assert Path(result.stdout.strip()) == tmp_path / "data" / "config.yaml"
    assert "ignoring HERMES_HOME" in result.stderr


@pytest.mark.platforms("posix")
def test_lina_worker_from_a_hermes_agent_terminal_names_that_agent_quietly(tmp_path):
    """A Hermes Agent terminal's ``HERMES_HOME`` is a profile under the account's Hermes Agent home (found from
    the password database, never ``$HOME``). Only its name is read: the profile is never created."""
    user = tmp_path / "user"
    user.mkdir()
    brief = tmp_path / "brief.md"
    brief.write_text("do it\n", encoding="utf-8")
    profile = hermes_constants.hermes_agent_home() / "profiles" / "agent-b-lina434-test"
    assert not profile.exists()

    done = _lina(["worker", "submit", "--brief", str(brief), "--topic", "caller-check"], user,
                 HERMES_HOME=str(profile))

    assert done.returncode == 0, done.stderr
    assert "ignoring HERMES_HOME" not in done.stderr
    task_id = done.stdout.split()[0]
    job = (user / ".lina" / "worker" / "hub" / "jobs" / f"{task_id}.json").read_text(encoding="utf-8")
    assert json.loads(job)["bot"] == "agent-b-lina434-test"
    assert not profile.exists()
