"""Create result repositories through the CLI and the agent's registered tool."""

import json
from pathlib import Path
import sqlite3
import subprocess
import sys

from tests.hermes_cli._work_env import WorkEnv


def _config(work, root, command):
    work.config(extra=(
        "  forge:\n"
        f"    url: '{root.as_uri()}/{{repo}}.git'\n"
        f"    create_command: {json.dumps(command)}\n"
    ))


def _cli(repo):
    return subprocess.run(
        [sys.executable, str(Path(__file__).resolve().parents[2] / "lina"), "repo", "create", repo],
        capture_output=True, text=True, check=False,
    )


def test_cli_and_agent_share_one_registered_bare_repository(tmp_path, monkeypatch):
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos(todos=0)
    root = tmp_path / "local forge"
    _config(work, root, ["git", "init", "--bare", str(root / "{repo}.git")])
    result = _cli("A1/album")
    assert result.returncode == 0, result.stderr
    remote = root / "A1/album.git"
    assert subprocess.check_output(
        ["git", "-C", str(remote), "rev-parse", "--is-bare-repository"], text=True,
    ).strip() == "true"

    from tools import repo_tool
    from tools.registry import registry

    _config(work, root, [sys.executable, "-c", "raise SystemExit(1)", "{repo}"])
    assert repo_tool.available()
    assert json.loads(registry.dispatch("repo_create", {"repo": "A1/album"})) == json.loads(result.stdout)
    with sqlite3.connect(work.work / "work.db") as db:
        assert db.execute("SELECT id, area_id, name, forge_url FROM repo").fetchall() == [
            ("A1/album", "A1", "album", remote.as_uri()),
        ]


def test_failed_provisioning_can_be_retried_and_invalid_targets_never_reach_forge(tmp_path, monkeypatch):
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos(todos=0)
    root = tmp_path / "forge"
    _config(work, root, [sys.executable, "-c", "raise SystemExit(19)", "{repo}"])
    failed = _cli("A1/album")
    assert failed.returncode and "19" in failed.stderr
    with sqlite3.connect(work.work / "work.db") as db:
        assert db.execute("SELECT count(*) FROM repo").fetchone() == (0,)
    _config(work, root, ["git", "init", "--bare", str(root / "{repo}.git")])
    for target in ("A9/album", "A1/../escape"):
        assert _cli(target).returncode
    assert not root.exists()
    assert _cli("A1/album").returncode == 0
