"""Real-repository regression coverage for worker #228 artifact boundaries."""

import io
import os
import subprocess
import tarfile
import tempfile
from pathlib import Path

import pytest

from hermes_cli.worker import device
from hermes_cli.worker.runners import base
from hermes_cli.worker.runners.base import Task, standard_result
from hermes_cli.worker.task import git_summary, sha256, write_manifest


# These deliberate Git integration tests own disposable repos under out/. The
# path-based live guard otherwise mistakes those nested repos for this checkout.
pytestmark = pytest.mark.live_system_guard_bypass


@pytest.fixture
def workspace():
    root = Path(__file__).resolve().parents[2] / "out" / ".artifact-tmp"
    root.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=root) as directory:
        yield Path(directory)


def _git(repo: Path, *args: str) -> str:
    env = {**os.environ, "GIT_MASTER": "1", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
           "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid",
           "GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid"}
    return subprocess.run(["git", "-C", str(repo), *args], env=env, check=True, capture_output=True,
                          text=True, timeout=60).stdout.strip()


def _repo_task(workspace: Path) -> Task:
    repo = workspace / "repo"
    repo.mkdir()
    _git(repo, "init", "-q", "-b", "main")
    _git(repo, "commit", "-q", "--allow-empty", "-m", "base")
    task = Task("OT-ARTIFACT", workspace / "task")
    task.dir.mkdir()
    _git(repo, "worktree", "add", "-q", "-b", "task", str(task.work))
    task.path("base").write_text(_git(repo, "rev-parse", "HEAD"), encoding="utf-8")
    return task


def test_manifest_excludes_tool_directories_without_traversing_them(workspace, monkeypatch):
    # Given real files and tool directories at several depths in a real worktree.
    task = _repo_task(workspace)
    out = task.work / "out"
    junk = (".scratch", ".venv", "venv", ".pytest_cache", "__pycache__", "node_modules",
            ".mypy_cache", ".ruff_cache", ".tox", ".git")
    for parent in (out, out / "nested"):
        for name in junk:
            path = parent / name / "must-not-hash.txt"
            path.parent.mkdir(parents=True)
            path.write_text("temporary", encoding="utf-8")
    artifact = out / "nested" / "result.txt"
    artifact.write_text("delivered", encoding="utf-8")
    # A file with a directory-only exclusion name remains a deliverable.
    named_file = out / "nested" / "deeper" / "venv"
    named_file.parent.mkdir()
    named_file.write_text("delivered too", encoding="utf-8")
    expected = {"out/nested/result.txt": sha256(artifact), "out/nested/deeper/venv": sha256(named_file)}
    scanned = []
    scandir = os.scandir

    def record_scan(path):
        scanned.append(Path(path))
        return scandir(path)

    # When the device creates the delivery manifest.
    with monkeypatch.context() as patch:
        patch.setattr(os, "scandir", record_scan)
        manifest = write_manifest(task, 1)
    # Then excluded trees were neither visited nor hashed.
    assert manifest["files"] == expected
    assert manifest["deliverables"] == 2
    assert not any(path.name in junk for path in scanned)
    assert set(base.iter_artifact_files(out)) == {artifact, named_file}


@pytest.mark.platforms("posix")
def test_artifact_iterator_omits_symlinks_and_preserves_regular_files(workspace):
    # Given links to files and directories, including one inside an excluded tree.
    task = _repo_task(workspace)
    out = task.work / "out"
    out.mkdir()
    artifact = out / "result.txt"
    artifact.write_text("delivered", encoding="utf-8")
    (out / "file-link").symlink_to(artifact)
    (out / "directory-link").symlink_to(workspace, target_is_directory=True)
    (out / "broken-link").symlink_to(out / "missing")
    # When collecting runner results.
    files = standard_result(task)
    # Then only regular artifacts can be fetched and hashed.
    assert files == {"out/result.txt": artifact}


def test_git_summary_includes_only_worktrees_inside_task_folder(workspace):
    # Given sibling and detached worktrees with new commits, plus a prefix lookalike outside.
    task = _repo_task(workspace)
    siblings = ((task.dir / "sibling with spaces", "sibling", "sibling-change"),
                (task.dir / "nested" / "detached", None, "detached-change"),
                (workspace / "task-outside", "outside", "outside-change"))
    heads = {}
    for path, branch, message in siblings:
        options = ("-b", branch) if branch else ("--detach",)
        _git(task.work, "worktree", "add", "-q", *options, str(path), "HEAD")
        _git(path, "commit", "-q", "--allow-empty", "-m", message)
        heads[message] = _git(path, "rev-parse", "--short", "HEAD")
    # When producing the summary used for git.txt.
    summary = git_summary(task)
    # Then in-task worktrees expose their branch/head and new commits, never outside ones.
    assert "branch: task\n" in summary
    assert "sibling with spaces" in summary and "sibling" in summary
    for message in ("sibling-change", "detached-change"):
        assert message in summary and heads[message] in summary
    assert "nested/detached" in summary
    assert "outside-change" not in summary
    assert "task-outside" not in summary
    assert heads["outside-change"] not in summary


def test_pack_never_fetches_tool_trees(workspace):
    worker = workspace / "worker"
    task = device.task_of(worker, "OT-PACK")
    out = task.work / "out"
    for name in (".scratch", ".venv", "node_modules", ".pytest_cache"):
        folder = out / name
        folder.mkdir(parents=True)
        (folder / "hidden").write_text("not a deliverable")
    (out / "result.txt").write_text("delivered")
    with tarfile.open(fileobj=io.BytesIO(device.pack(worker, task.id))) as archive:
        assert archive.getnames() == ["out/result.txt"]
        assert archive.extractfile("out/result.txt").read() == b"delivered"
