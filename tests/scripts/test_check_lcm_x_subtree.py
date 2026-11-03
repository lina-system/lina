"""scripts/ci/check_lcm_x_subtree.py against a throwaway repo shaped like the squashed LCM-X subtree (lina#15)."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "ci" / "check_lcm_x_subtree.py"
PREFIX = Path("plugins") / "context_engine" / "lcm-x"
LEDGER = Path("plugins") / "context_engine" / "lcm-x-intake.md"

LICENSE = b"MIT License\n\nCopyright (c) 2026 Voltropy\n"
SOURCE = b"# Copyright (c) 2026 Voltropy\nVALUE = 1\n"
README = b"# LCM-X\n"


def _git(repo: Path, *args: str) -> str:
    return subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@example.invalid", *args], cwd=repo,
                          capture_output=True, text=True, encoding="utf-8", check=True, timeout=60).stdout.strip()


def _commit(repo: Path, files: dict[str, bytes], message: str) -> str:
    for rel, data in files.items():
        (repo / rel).parent.mkdir(parents=True, exist_ok=True)
        (repo / rel).write_bytes(data)
    _git(repo, "add", ".")
    _git(repo, "commit", "-qm", message)
    return _git(repo, "rev-parse", "HEAD")


@pytest.fixture
def repo(tmp_path: Path) -> tuple[Path, str]:
    up = tmp_path / "upstream"
    up.mkdir()
    _git(up, "init", "-q")
    upstream_sha = _commit(up, {"LICENSE": LICENSE, "engine.py": SOURCE, "README.md": README}, "upstream")

    root = tmp_path / "lina"
    root.mkdir()
    _git(root, "init", "-q")
    _git(root, "fetch", "-q", str(up), "HEAD")
    _git(root, "branch", "mirror", upstream_sha)
    tree = _git(root, "rev-parse", f"{upstream_sha}^{{tree}}")
    squash = _git(root, "commit-tree", tree, "-m", f"Squashed '{PREFIX.as_posix()}/' content\n\n"
                  f"git-subtree-dir: {PREFIX.as_posix()}\ngit-subtree-split: {upstream_sha}")
    _git(root, "reset", "-q", "--hard", squash)
    _git(root, "read-tree", f"--prefix={PREFIX.as_posix()}/", "-u", squash)
    (root / LEDGER).write_text(
        "# Ledger\n\n## Intakes\n\n| Date | Ref | Commit | Method |\n| --- | --- | --- | --- |\n"
        f"| 2026-11-02 | v0.29.1-rc2 | {upstream_sha} | add |\n\n## LINA changes\n\n| Path | Change | Fork commit |\n",
        encoding="utf-8")
    _git(root, "add", ".")
    _git(root, "commit", "-qm", "import")
    # A LINA change on top of the squash, as the fork patches are.
    (root / PREFIX / "engine.py").write_bytes(SOURCE + b"VALUE = 2\n")
    _git(root, "commit", "-qam", "feat(lcm-x): a LINA change")
    return root, upstream_sha


def _run(root: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run([sys.executable, str(SCRIPT), "--repo", str(root), *args], capture_output=True, text=True,
                          encoding="utf-8", timeout=120, stdin=subprocess.DEVNULL, check=False)


def test_consistent_subtree_with_lina_changes_and_no_notice_passes(repo):
    root, _ = repo
    result = _run(root, "--mirror", "mirror")
    assert result.returncode == 0, result.stdout + result.stderr
    assert "plugins/context_engine/lcm-x: subtree, ledger, license files and headers are consistent" in result.stdout


def test_changed_license_fails(repo):
    root, _ = repo
    (root / PREFIX / "LICENSE").write_bytes(LICENSE + b"Modified by LINA\n")
    result = _run(root)
    assert result.returncode == 1
    assert "plugins/context_engine/lcm-x/LICENSE: license file differs from upstream" in result.stdout


def test_removed_header_line_fails(repo):
    root, _ = repo
    (root / PREFIX / "engine.py").write_bytes(b"VALUE = 1\n")
    result = _run(root)
    assert result.returncode == 1
    assert "plugins/context_engine/lcm-x/engine.py: upstream header line changed or removed" in result.stdout


def test_ledger_not_matching_the_newest_squash_fails(repo):
    root, upstream_sha = repo
    ledger = root / LEDGER
    ledger.write_text(ledger.read_text(encoding="utf-8").replace(upstream_sha, "f" * 40), encoding="utf-8")
    result = _run(root)
    assert result.returncode == 1
    assert f"is of {upstream_sha}" in result.stdout


def test_upstream_commit_missing_from_the_mirror_fails(repo):
    root, upstream_sha = repo
    (root / "empty").write_bytes(b"")
    empty_tree = _git(root, "hash-object", "-t", "tree", "-w", "empty")
    _git(root, "branch", "other-mirror", _git(root, "commit-tree", empty_tree, "-m", "unrelated upstream history"))
    result = _run(root, "--mirror", "other-mirror")
    assert result.returncode == 1
    assert f"upstream commit {upstream_sha} is not on the mirror other-mirror" in result.stdout


def test_squash_that_is_not_upstreams_tree_fails(repo):
    root, upstream_sha = repo
    # A squash that already holds a LINA patch: the next `git subtree pull` would revert the patch.
    patched = _git(root, "rev-parse", f"HEAD:{PREFIX.as_posix()}")
    squash = _git(root, "commit-tree", patched, "-p", "HEAD", "-m", f"Squashed '{PREFIX.as_posix()}/' content\n\n"
                  f"git-subtree-dir: {PREFIX.as_posix()}\ngit-subtree-split: {upstream_sha}")
    _git(root, "reset", "-q", "--soft", squash)
    result = _run(root, "--mirror", "mirror")
    assert result.returncode == 1
    assert "does not hold the tree of upstream commit" in result.stdout
