"""Concurrent and cut-off landings of one branch against real Git repositories (lina#899).

``land()`` is driven through the same real clone, fetch, merge, push and landing checks as
``test_work_land.py``; only GitHub is a fake. A caller that waits for the current landing of a branch must end
with the verdict of the open pull request, like the caller that opened it, and a reader of the progress output
that goes away must not end the landing."""
from __future__ import annotations

import os
import subprocess
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor

import pytest

from hermes_cli import merge_rules as rules
from hermes_cli.delivery.land import land
from tests.hermes_cli.test_work_land import BRANCH, TASK, commit, fake_github, git, world

HEAD = "f" * 40
FAILED_CHECK = {"id": 1, "name": "tests", "status": "completed", "conclusion": "failure",
                "completed_at": "2026-11-03T09:00:00Z"}


def gate(gh, state: str) -> int:
    """Put the open pull request's head checks in *state*; the code ``report_only`` answers for it."""
    if state == "failing":
        gh.routes[f"repos/acme/app/commits/{HEAD}/check-runs"] = {"total_count": 1, "check_runs": [FAILED_CHECK]}
        return rules.FAIL
    return rules.WAIT


def pr_creates(gh) -> int:
    return len([c for c in gh.commands if c[:2] == ["pr", "create"]])


@pytest.mark.parametrize("state", ["waiting", "failing"])
def test_the_caller_that_waited_ends_with_the_verdict_of_the_open_pull_request(world, state):
    commit(world["work"], "app/feature.py", "VALUE = 2\n", "feat(app): the feature")
    gh = fake_github()
    expected = gate(gh, state)
    publish, creating, release = gh.on_run, threading.Event(), threading.Event()

    def hold_creation(args):
        if args[:2] == ["pr", "create"]:
            creating.set()
            assert release.wait(30)
        return publish(args)

    gh.on_run = hold_creation
    lines = []

    def run(**options):
        return land(gh, BRANCH, TASK, "feat(app): the feature", 12, base=world["base"], **options)

    with ThreadPoolExecutor(max_workers=2) as pool:
        first = pool.submit(run, out=lambda _line: None)
        assert creating.wait(30)
        second = pool.submit(run, out=lines.append)
        deadline = time.monotonic() + 30
        while not any("waiting" in line for line in lines) and time.monotonic() < deadline:
            time.sleep(0.02)
        release.set()
        opened, waited = first.result(timeout=60), second.result(timeout=60)

    assert pr_creates(gh) == 1
    assert any("already open" in line for line in lines), lines
    assert opened == expected, "the caller that opened the pull request answers with its gates"
    assert waited == expected, "the caller that waited answers with the same gates, never a plain 0"


def land_with_a_reader_that_leaves_after(world, phrase: str):
    """Run ``land()`` with its default progress output on a pipe whose reader closes once *phrase* has arrived."""
    gh = fake_github()
    reader, writer = os.pipe()
    seen = []

    def read():
        text = b""
        while phrase.encode() not in text:
            chunk = os.read(reader, 4096)
            if not chunk:
                break
            text += chunk
        seen.append(text.decode())
        os.close(reader)

    thread = threading.Thread(target=read)
    thread.start()
    stdout, sys.stdout = sys.stdout, os.fdopen(writer, "w")
    try:
        code = land(gh, BRANCH, TASK, "feat(app): the feature", 12, base=world["base"])
        sys.stdout.flush()  # what the interpreter does at exit: it must not fail either
    finally:
        sys.stdout.close()
        sys.stdout = stdout
        thread.join()
    return gh, code, seen[0]


@pytest.mark.parametrize("phrase", ["cloning", "pushing"])
def test_a_progress_reader_that_goes_away_does_not_end_the_landing(world, phrase):
    commit(world["work"], "app/feature.py", "VALUE = 2\n", "feat(app): the feature")

    gh, code, seen = land_with_a_reader_that_leaves_after(world, phrase)

    assert phrase in seen
    assert pr_creates(gh) == 1, "the landing went on to open the pull request"
    assert code == rules.WAIT, "the exit code is the landing's own: no checks reported on the new head yet"
    pushed = git(world["origin"], "for-each-ref", "--format=%(refname)", f"refs/heads/{BRANCH}")
    assert pushed == f"refs/heads/{BRANCH}"


def test_a_cut_session_leaves_the_exit_code_of_the_command_to_the_landing(world, tmp_path):
    """The whole command: stdout closed by the reader before the first line, exit code read from the process."""
    commit(world["work"], "app/feature.py", "VALUE = 2\n", "feat(app): the feature")
    script = tmp_path / "cut.py"
    script.write_text(
        "import os, sys\n"
        "from pathlib import Path\n"
        "from tests.hermes_cli import test_work_land as fx\n"
        "from hermes_cli.delivery.land import land\n"
        "reader, writer = os.pipe(); os.close(reader)\n"
        "os.dup2(writer, 1)\n"
        f"code = land(fx.fake_github(), {BRANCH!r}, {TASK!r}, 'feat(app): the feature', 12, base=Path({str(world['base'])!r}))\n"
        "raise SystemExit(code)\n", encoding="utf-8")
    done = subprocess.run([sys.executable, str(script)], capture_output=True, text=True, timeout=120, check=False,
                          cwd=os.getcwd(), env={**os.environ, "PYTHONPATH": os.getcwd()})
    assert done.returncode == rules.WAIT, (done.returncode, done.stderr[-800:])
