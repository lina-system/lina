"""A run starts LINA's pinned copy of a runner once it is installed, else the one on PATH (lina#238).

Each case builds the runner the way a run and ``probe`` do (``runners.build`` with the LINA root), asks
``available()`` and really starts it: the started script writes the path it was executed as, so the spawned argv is
read from what ran, not from the code.
"""

from __future__ import annotations

import json
import os

import pytest

from hermes_cli import lina_runners
from hermes_cli.worker.runners import build
from hermes_cli.worker.runners.base import Task
from tests.waiting import wait_until

pytestmark = pytest.mark.platforms("posix")

PINNED = "codex"
NOT_PINNED = "lina-dsh-acp"
RECORD = "#!/bin/sh\nprintf '%s' \"$0\" > \"$PWD/ran.txt\"\n"


def _script(path):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(RECORD, encoding="utf-8")
    path.chmod(0o755)
    return path


@pytest.fixture
def place(tmp_path, monkeypatch):
    root = tmp_path / "lina"
    on_path = tmp_path / "person-bin"
    on_path.mkdir()
    monkeypatch.setenv("PATH", os.pathsep.join([str(on_path), "/usr/bin", "/bin"]))
    return root, on_path


def _start(kind, argv, root, tmp_path):
    spec = {"type": kind, "argv": argv}
    if kind == "command":
        spec["access"] = "workspace-write"
    runner = build("probe", spec, {"stall_grace_s": 10}, root)
    ready = runner.available()
    task = Task("lina-copy", tmp_path / "task")
    task.work.mkdir(parents=True)
    handle = runner.start(task, "hello", None, access="workspace-write")
    try:
        ran = task.work / "ran.txt"
        wait_until(lambda: ran.exists() and ran.read_text(), timeout=15, what="the runner started")
        launched = [json.loads(line) for line in task.path("events.1.jsonl").read_text().splitlines()]
    finally:
        runner.stop(handle)
    return ready, ran.read_text(), next(event["argv"] for event in launched if event["kind"] == "launched")


def test_the_names_cases_use_are_what_the_pins_say():
    pinned = lina_runners.pins(moved=False)
    assert PINNED in pinned and NOT_PINNED not in pinned


@pytest.mark.parametrize("kind", ["acp", "command"])
def test_an_installed_lina_copy_is_what_runs(place, tmp_path, kind):
    root, on_path = place
    shim = _script(lina_runners.shim_path(root, PINNED))
    _script(on_path / PINNED)

    ready, ran, launched = _start(kind, [PINNED], root, tmp_path)

    assert ready == (True, str(shim))
    assert ran == str(shim) and launched == [str(shim)]


@pytest.mark.parametrize("kind", ["acp", "command"])
def test_without_a_lina_copy_the_path_copy_runs(place, tmp_path, kind):
    root, on_path = place
    person = _script(on_path / PINNED)

    ready, ran, launched = _start(kind, [PINNED], root, tmp_path)

    assert ready == (True, str(person))
    assert ran == str(person) and launched == [PINNED]


@pytest.mark.parametrize("kind", ["acp", "command"])
def test_a_command_given_as_a_path_is_left_alone(place, tmp_path, kind):
    root, on_path = place
    _script(lina_runners.shim_path(root, PINNED))
    person = _script(on_path / PINNED)

    ready, ran, launched = _start(kind, [str(person)], root, tmp_path)

    assert ready == (True, str(person))
    assert ran == str(person) and launched == [str(person)]


@pytest.mark.parametrize("kind", ["acp", "command"])
def test_a_name_lina_does_not_pin_stays_on_path(place, tmp_path, kind):
    root, on_path = place
    _script(lina_runners.shim_path(root, NOT_PINNED))
    person = _script(on_path / NOT_PINNED)

    ready, ran, launched = _start(kind, [NOT_PINNED], root, tmp_path)

    assert ready == (True, str(person))
    assert ran == str(person) and launched == [NOT_PINNED]
