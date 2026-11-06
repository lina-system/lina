"""The installed ``omo-task`` shim as the agents call it (lina-system/lina#287), run as a real process.

The shim always runs in the LINA root it was installed for, whatever ``HERMES_HOME`` the caller has; that
``HERMES_HOME`` (Hermes Agent's profile home) names the owner of a run. ``list`` shows ``lina worker``'s tasks and
then the old tool's, and every verb that names an old ID, ``attach`` and ``card`` included, goes to the old tool.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import textwrap

import pytest

from hermes_cli import lina_shims


@pytest.fixture
def installed(tmp_path):
    """A non-default LINA root with its shims, a Hermes Agent profile home, and the old tool beside them."""
    root, user = tmp_path / "data" / "lina-root", tmp_path / "user"
    root.mkdir(parents=True)
    user.mkdir()
    legacy = tmp_path / "old-omo-task"
    legacy.write_text(textwrap.dedent(f"""\
        #!{sys.executable}
        import sys
        print("old tool:", " ".join(sys.argv[1:]))
        """), encoding="utf-8")
    legacy.chmod(0o755)
    (root / "config.yaml").write_text(f"worker:\n  legacy_omo_task: {legacy}\n", encoding="utf-8")
    assert lina_shims.install(root, out=lambda line: None) == 0
    hermes = tmp_path / "hermes-root" / "profiles" / "agent-b"
    hermes.mkdir(parents=True)

    def run(*args: str) -> subprocess.CompletedProcess:
        env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(user), "LANG": "C.UTF-8",
               "HERMES_HOME": str(hermes)}
        return subprocess.run([str(root / "bin" / "omo-task"), *args], capture_output=True, text=True, env=env,
                              timeout=300, check=False)
    return root, tmp_path, run


def _without_age(text: str) -> str:
    """*text* with each ``handed out <age> ago`` read at its own moment replaced by one placeholder."""
    return re.sub(r"handed out (?:\d+h \d+m|\d+m|\d+s|-) ago", "handed out <age> ago", text)


@pytest.mark.platforms("posix")
def test_the_shim_keeps_its_root_whatever_hermes_home_says_and_that_home_names_the_owner(installed):
    root, tmp, run = installed
    brief = tmp / "brief.md"
    brief.write_text("do it\n", encoding="utf-8")
    done = run("submit", "--brief", str(brief), "--topic", "switch-check")
    assert done.returncode == 0, done.stderr
    task_id = done.stdout.split()[0]
    job = json.loads((root / "worker" / "hub" / "jobs" / f"{task_id}.json").read_text(encoding="utf-8"))
    assert job["bot"] == "agent-b"
    assert "ignoring HERMES_HOME" not in done.stderr
    status = run("status", task_id)
    assert status.returncode == 0 and status.stdout.startswith(f"{task_id} · agent-b · switch-check")
    attach = run("attach", task_id)
    assert (attach.returncode, _without_age(attach.stdout)) == (0, _without_age(status.stdout))
    assert run("card", task_id).stdout == f"{task_id}: no card\n"
    assert run("accept", task_id, "--note", "fine").returncode == 0
    receipts = [json.loads(line) for line in (root / "worker" / "hub" / "receipts.jsonl").read_text("utf-8").splitlines()]
    assert [r["by"] for r in receipts if r["kind"] == "verdict"] == ["agent-b"]


@pytest.mark.platforms("posix")
def test_list_shows_lina_workers_tasks_then_the_old_tools_and_old_ids_go_to_the_old_tool(installed):
    _, tmp, run = installed
    brief = tmp / "brief.md"
    brief.write_text("do it\n", encoding="utf-8")
    task_id = run("submit", "--brief", str(brief), "--topic", "listed").stdout.split()[0]
    listed = run("list", "--all")
    assert listed.returncode == 0, listed.stderr
    lines = listed.stdout.splitlines()
    assert lines[0].startswith(f"{task_id}  agent-b  listed") and lines[-1] == "old tool: list --all"
    for verb in (["attach", "OT-OLD1106000000"], ["card", "OT-OLD1106000000"], ["card", "OT-OLD1106000000", "--drop",
                                                                             "t_card1"]):
        assert run(*verb).stdout == f"old tool: {' '.join(verb)}\n"
