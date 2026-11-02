"""The shared Desk (lina-system/lina#44): one folder every agent starts in, named by one setting."""

import json
import subprocess
import sys
from pathlib import Path

import pytest

from hermes_cli import desk as desk_mod
from hermes_cli.desk import DeskError, init_desk, resolve_desk_ref, to_desk_ref


@pytest.fixture
def root_home(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setenv("HOME", str(tmp_path))  # expanduser("~/Desk") reads HOME, not Path.home
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.delenv("TERMINAL_CWD", raising=False)
    return home


def _profile(root_home: Path, name: str, config: str = "") -> Path:
    home = root_home / "profiles" / name
    home.mkdir(parents=True)
    (home / "config.yaml").write_text(config, encoding="utf-8")
    return home


def test_init_creates_the_layout_and_every_profile_reads_the_one_setting(root_home, tmp_path, monkeypatch):
    desk = init_desk(tmp_path / "Desk")

    assert sorted(p.name for p in desk.iterdir()) == ["AGENTS.md", "Archive", "Areas", "Inbox", "Projects", "Resources"]
    monkeypatch.setenv("HERMES_HOME", str(_profile(root_home, "coder", "desk:\n  path: /elsewhere\n")))
    assert desk_mod.get_desk_path() == desk


def test_adopt_keeps_files_and_numbered_folders_and_refuses_without_adopt(root_home, tmp_path):
    old = tmp_path / "old"
    (old / "1_Projects").mkdir(parents=True)
    (old / "AGENTS.md").write_text("mine", encoding="utf-8")

    with pytest.raises(DeskError):
        init_desk(old)
    init_desk(old, adopt=True)

    assert sorted(p.name for p in old.iterdir()) == ["1_Projects", "AGENTS.md", "Archive", "Areas", "Inbox", "Resources"]
    assert (old / "AGENTS.md").read_text(encoding="utf-8") == "mine"
    assert desk_mod.desk_info()["folders"]["Projects"] == "1_Projects"


def test_a_rerun_without_a_path_keeps_the_configured_desk(root_home, tmp_path):
    """Regression for lina-system/lina#76: both installers rerun `desk init --adopt` with no path,
    so a reinstall must not move a Desk made with `desk init --path` back to ~/Desk."""
    custom = tmp_path / "Documents" / "Desk"
    init_desk(custom)
    configured = (root_home / "config.yaml").read_text(encoding="utf-8")

    assert init_desk(adopt=True) == custom
    assert desk_mod.configured_desk_path() == custom
    assert (root_home / "config.yaml").read_text(encoding="utf-8") == configured
    assert not (tmp_path / "Desk").exists()
    assert sorted(p.name for p in custom.iterdir()) == ["AGENTS.md", "Archive", "Areas", "Inbox", "Projects", "Resources"]


def test_refs_stay_on_the_desk(root_home, tmp_path):
    desk = init_desk(tmp_path / "Desk")
    (tmp_path / "secret.txt").write_text("x", encoding="utf-8")
    (desk / "Inbox" / "link").symlink_to(tmp_path / "secret.txt")

    assert resolve_desk_ref("desk:Projects/a.md") == (desk / "Projects" / "a.md").resolve()
    assert to_desk_ref(desk / "Projects" / "a.md") == "desk:Projects/a.md"
    assert to_desk_ref(tmp_path / "secret.txt") is None
    for bad in ("desk:../secret.txt", "desk:Inbox/link"):
        with pytest.raises(DeskError):
            resolve_desk_ref(bad)


def test_agent_workdir_defaults_to_the_desk_and_an_explicit_cwd_wins(root_home, tmp_path):
    from tools.terminal_scope import build_profile_terminal_scope

    plain = _profile(root_home, "plain")
    pinned = _profile(root_home, "pinned", f"terminal:\n  cwd: {tmp_path}\n")
    assert build_profile_terminal_scope(plain)["TERMINAL_CWD"] == str(tmp_path)  # no Desk yet: home

    desk = init_desk(tmp_path / "Desk")

    assert build_profile_terminal_scope(plain)["TERMINAL_CWD"] == str(desk)
    assert build_profile_terminal_scope(root_home)["TERMINAL_CWD"] == str(desk)
    assert build_profile_terminal_scope(pinned)["TERMINAL_CWD"] == str(tmp_path)


def test_a_standalone_gateway_starts_on_the_desk(root_home, tmp_path, monkeypatch):
    import os

    from gateway.run_bootstrap import _start_gateway_resolve_terminal_cwd

    desk = init_desk(tmp_path / "Desk")
    monkeypatch.setenv("TERMINAL_ENV", "local")
    monkeypatch.setenv("TERMINAL_CWD", ".")

    _start_gateway_resolve_terminal_cwd()

    assert os.environ["TERMINAL_CWD"] == str(desk)


def test_client_api_names_the_desk_and_resolves_refs(root_home, tmp_path):
    from starlette.testclient import TestClient

    from hermes_cli import web_server

    desk = init_desk(tmp_path / "Desk")
    web_server.app.state.auth_required = False
    client = TestClient(web_server.app)
    client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN

    info = client.get("/api/desk").json()
    assert info["path"] == str(desk) and info["folders"]["Inbox"] == "Inbox"
    assert client.get("/api/fs/default-cwd").json()["cwd"] == str(desk)
    resolved = client.get("/api/desk/resolve", params={"ref": "desk:Inbox"}).json()
    assert resolved == {"ref": "desk:Inbox", "path": str(desk / "Inbox"), "exists": True, "is_dir": True}
    assert client.get("/api/desk/resolve", params={"ref": "desk:../x"}).status_code == 400


def test_a_task_workspace_can_point_to_a_desk_folder(root_home, tmp_path):
    from hermes_cli.kanban_db import Task
    from hermes_cli.kanban_db_workspace import resolve_workspace

    desk = init_desk(tmp_path / "Desk")
    fields = {name: None for name in Task.__dataclass_fields__}
    task = Task(**dict(fields, id="t1", workspace_kind="dir", workspace_path="desk:Projects/launch"))

    assert resolve_workspace(task) == desk / "Projects" / "launch"
    with pytest.raises(DeskError):
        resolve_workspace(Task(**dict(fields, id="t2", workspace_kind="dir", workspace_path="desk:../out")))


@pytest.mark.platforms("posix")
def test_a_desk_write_holds_a_lock_other_processes_see(root_home, tmp_path):
    from tools import file_state

    desk = init_desk(tmp_path / "Desk")
    target = str((desk / "Inbox" / "note.md").resolve())
    probe = ("import fcntl, sys\nf = open(sys.argv[1], 'a+')\ntry:\n    fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)\n"
             "    print('free')\nexcept BlockingIOError:\n    print('held')\n")
    lock_file = desk_mod.desk_lock_file(target)

    with file_state.lock_path(target):
        held = subprocess.run([sys.executable, "-c", probe, str(lock_file)], capture_output=True, text=True, check=True)
    free = subprocess.run([sys.executable, "-c", probe, str(lock_file)], capture_output=True, text=True, check=True)

    assert (held.stdout.strip(), free.stdout.strip()) == ("held", "free")
    assert desk_mod.desk_lock_file(tmp_path / "elsewhere.md") is None


def test_desk_cli_init_and_show(root_home, tmp_path, capsys):
    from hermes_cli.subcommands.desk import cmd_desk

    class Args:
        desk_action, path, adopt, json = "init", None, False, True

    assert cmd_desk(Args) == 0
    Args.desk_action = "show"
    capsys.readouterr()
    assert cmd_desk(Args) == 0
    assert json.loads(capsys.readouterr().out)["path"] == str(tmp_path / "Desk")
