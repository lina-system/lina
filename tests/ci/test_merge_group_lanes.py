"""Merge-queue runs of CI (scripts/ci/classify_changes.py, .github/workflows/ci.yml merge_group).

A queue entry is the last gate before dev: every test suite runs on it whatever the diff says,
and lanes bound to paths (helper, uv_lock, ...) still follow the diff. docs/delivery.md, step 7.
"""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

import pytest

_REPO = Path(__file__).resolve().parents[2]
_CLASSIFIER = _REPO / "scripts" / "ci" / "classify_changes.py"


def _run(event: str, paths: list[str]) -> dict[str, bool]:
    env = {k: v for k, v in os.environ.items() if k not in ("GITHUB_OUTPUT", "GITHUB_EVENT_PATH")}
    env["EVENT_NAME"] = event
    out = subprocess.run([sys.executable, str(_CLASSIFIER)], input="\n".join(paths) + "\n",
                         capture_output=True, text=True, env=env, cwd=_REPO, check=True, timeout=120)
    return {k: v == "true" for k, v in (line.split("=", 1) for line in out.stdout.splitlines() if "=" in line)}


@pytest.mark.parametrize("paths", [["README.md"], ["docs/ci.md"], ["gateway/run.py"]])
def test_queue_entry_runs_every_suite(paths):
    lanes = _run("merge_group", paths)
    assert lanes["python"] and lanes["python_prod"] and lanes["macos"]


def test_queue_entry_keeps_path_bound_lanes_from_the_diff():
    docs = _run("merge_group", ["README.md"])
    assert not docs["helper"] and not docs["uv_lock"]
    assert _run("merge_group", ["helper/macos/Makefile"])["helper"]


def test_pull_request_of_the_same_diff_stays_narrow():
    lanes = _run("pull_request_replay", ["README.md"])
    assert not lanes["python"] and not lanes["macos"]


def test_empty_queue_diff_fails_open():
    assert all(_run("merge_group", []).values())
