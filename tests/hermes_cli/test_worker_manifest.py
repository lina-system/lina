"""The manifest contract of ``lina worker`` (lina#35): the device hashes what a run delivered once it settled, the hub
checks the fetched files against it, so a deliverable changed after the manifest is a mismatch; inputs are copied
read-only into ``work/input`` and a changed input shows up as a mismatch too."""

import json
import os
import stat
import subprocess

import pytest

from hermes_cli.worker import device
from hermes_cli.worker.runners.base import Task
from hermes_cli.worker.task import end_state, lock_inputs, verify, write_manifest
from hermes_cli.worker.transport import LocalTransport, fetch, put, put_tree
from tests.hermes_cli._worker_fakes import REPORT_WITH_NOTES


def _task(root, task_id="OT-SIO1102120000"):
    task = Task(task_id, root / "tasks" / task_id)
    (task.work / "out").mkdir(parents=True)
    (task.work / "report.md").write_text(REPORT_WITH_NOTES, encoding="utf-8")
    (task.work / "out" / "ok.txt").write_text("ok\n", encoding="utf-8")
    return task


def test_a_deliverable_changed_after_the_manifest_is_a_mismatch(tmp_path):
    root = tmp_path / "worker"
    task = _task(root)
    manifest = write_manifest(task, 1)
    assert manifest["deliverables"] == 2 and manifest["next_notes"] == 1

    dest = tmp_path / "hub-out"
    fetch(LocalTransport(root), task.id, dest)
    assert verify(dest)["ok"] is True

    (task.work / "out" / "ok.txt").write_text("changed by a late background job\n", encoding="utf-8")
    fetch(LocalTransport(root), task.id, dest)
    check = verify(dest)
    assert check["ok"] is False and check["mismatch"] == ["out/ok.txt"]


def test_no_manifest_is_reported_as_missing(tmp_path):
    root = tmp_path / "worker"
    task = _task(root)
    dest = tmp_path / "hub-out"
    fetch(LocalTransport(root), task.id, dest)
    assert verify(dest)["error"] == "no manifest"


def test_inputs_are_read_only_on_the_device_and_a_changed_input_is_flagged(tmp_path):
    root = tmp_path / "worker"
    source = tmp_path / "notes"
    source.mkdir()
    (source / "a.txt").write_text("original\n", encoding="utf-8")
    transport = LocalTransport(root)
    put(transport, "OT-SIO1102120001", "job.json", "{}")
    put_tree(transport, "OT-SIO1102120001", "input.upload", [source])
    task = device.task_of(root, "OT-SIO1102120001")
    device.prepare(root, task)

    copied = task.work / "input" / "notes" / "a.txt"
    assert copied.read_text() == "original\n"
    assert not copied.stat().st_mode & (stat.S_IWUSR | stat.S_IWGRP | stat.S_IWOTH)
    assert not (task.work / "input" / "notes").stat().st_mode & stat.S_IWUSR
    assert write_manifest(task, 1)["inputs_unchanged"] is True

    os.chmod(copied.parent, 0o755)
    os.chmod(copied, 0o644)
    copied.write_text("edited by the agent\n", encoding="utf-8")
    assert write_manifest(task, 1)["inputs_unchanged"] is False


def _git(*args, cwd):
    env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.invalid", "GIT_COMMITTER_NAME": "t",
           "GIT_COMMITTER_EMAIL": "t@example.invalid"}
    return subprocess.run(["git", *args], cwd=cwd, env=env, capture_output=True, text=True, timeout=60, check=True)


def test_a_repo_task_works_in_a_fresh_worktree_without_push_rights(tmp_path):
    repo = tmp_path / "repo"
    repo.mkdir()
    _git("init", "-q", "-b", "main", cwd=repo)
    (repo / "a.txt").write_text("a\n")
    _git("add", "a.txt", cwd=repo)
    _git("commit", "-q", "-m", "base", cwd=repo)
    _git("remote", "add", "origin", str(tmp_path / "nowhere.git"), cwd=repo)
    root, tid = tmp_path / "worker", "OT-SIO1102120002"
    put(LocalTransport(root), tid, "job.json", json.dumps({"repo": str(repo), "branch": f"omo/{tid}",
                                                          "base_branch": "main"}))
    task = device.task_of(root, tid)
    device.prepare(root, task)

    assert _git("branch", "--show-current", cwd=task.work).stdout.strip() == f"omo/{tid}"
    push = subprocess.run(["git", "push", "origin", "HEAD"], cwd=task.work, capture_output=True, text=True, timeout=60,
                          check=False)
    assert push.returncode != 0 and "PUSH-BLOCKED" in push.stderr
    (task.work / "b.txt").write_text("b\n")
    (task.work / "report.md").write_text(REPORT_WITH_NOTES)
    _git("add", "-A", cwd=task.work)
    _git("commit", "-q", "-m", "feat: add b", cwd=task.work)
    assert "report.md" not in _git("show", "--name-only", "HEAD", cwd=task.work).stdout
    device.finish_run(root, task, 1, "done")
    summary = (task.dir / "git.txt").read_text()
    assert f"branch: omo/{tid}" in summary and "feat: add b" in summary
    assert (task.dir / "state").read_text().strip() == "done"


def test_the_push_block_stays_in_the_task_worktree(tmp_path):
    """The person's checkout and its other worktrees keep their push URL during and after a task."""
    repo = tmp_path / "repo"
    repo.mkdir()
    _git("init", "-q", "-b", "main", cwd=repo)
    (repo / "a.txt").write_text("a\n")
    _git("add", "a.txt", cwd=repo)
    _git("commit", "-q", "-m", "base", cwd=repo)
    _git("remote", "add", "origin", str(tmp_path / "remote.git"), cwd=repo)
    root, tid = tmp_path / "worker", "OT-SIO1102120003"
    put(LocalTransport(root), tid, "job.json", json.dumps({"repo": str(repo), "branch": f"omo/{tid}",
                                                          "base_branch": "main"}))
    task = device.task_of(root, tid)
    device.prepare(root, task)
    _git("worktree", "add", "-q", "-b", "sibling", str(tmp_path / "sibling"), "main", cwd=repo)

    def push_url(cwd):
        return _git("remote", "get-url", "--push", "origin", cwd=cwd).stdout.strip()

    assert push_url(task.work) == "PUSH-BLOCKED-by-lina-worker"
    assert push_url(repo) == str(tmp_path / "remote.git")
    assert push_url(tmp_path / "sibling") == str(tmp_path / "remote.git")


@pytest.mark.parametrize("local_branch", [False, True], ids=["only-on-origin", "stale-local"])
def test_a_base_branch_starts_from_its_head_on_origin(tmp_path, local_branch):
    """The repo cache is cloned once and then only fetched: a base branch that exists only on origin, or whose local
    branch fell behind, starts the task from origin's head."""
    origin, seed, repo = tmp_path / "origin.git", tmp_path / "seed", tmp_path / "repo"
    _git("init", "-q", "--bare", "-b", "main", str(origin), cwd=tmp_path)
    seed.mkdir()
    _git("init", "-q", "-b", "main", cwd=seed)
    (seed / "a.txt").write_text("a\n")
    _git("add", "a.txt", cwd=seed)
    _git("commit", "-q", "-m", "base", cwd=seed)
    _git("push", "-q", str(origin), "main:refs/heads/main", "main:refs/heads/release", cwd=seed)
    _git("clone", "-q", str(origin), str(repo), cwd=tmp_path)
    if local_branch:
        _git("branch", "release", "origin/release", cwd=repo)
    (seed / "b.txt").write_text("b\n")
    _git("add", "b.txt", cwd=seed)
    _git("commit", "-q", "-m", "release moves on", cwd=seed)
    _git("push", "-q", str(origin), "main:refs/heads/release", cwd=seed)
    root, tid = tmp_path / "worker", "OT-SIO1102120004"
    put(LocalTransport(root), tid, "job.json", json.dumps({"repo": str(repo), "branch": f"omo/{tid}",
                                                          "base_branch": "release"}))
    task = device.task_of(root, tid)
    device.prepare(root, task)

    assert _git("rev-parse", "HEAD", cwd=task.work).stdout == _git("rev-parse", "HEAD", cwd=seed).stdout


def test_lock_inputs_without_inputs_records_nothing(tmp_path):
    task = _task(tmp_path / "worker")
    assert lock_inputs(task) == {}
    assert "inputs_unchanged" not in write_manifest(task, 1)


def test_the_ending_state_follows_what_was_delivered():
    full = {"deliverables": 2, "next_notes": 1}
    assert end_state("done", full, cancelled=False, next_notes_required=True) == "done"
    assert end_state("done", {"deliverables": 0}, cancelled=False, next_notes_required=True) == "done_empty"
    assert end_state("done", {"deliverables": 1, "next_notes": None}, cancelled=False,
                     next_notes_required=True) == "review"
    assert end_state("done", {"deliverables": 1, "next_notes": 0}, cancelled=False, next_notes_required=True) == "review"
    assert end_state("stalled", full, cancelled=False, next_notes_required=True) == "stalled"
    assert end_state("done", full, cancelled=True, next_notes_required=True) == "cancelled"
