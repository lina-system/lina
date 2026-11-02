"""scripts/ci/check_honcho_subtree.py against a throwaway repo shaped like a squashed git subtree."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "ci" / "check_honcho_subtree.py"

LICENSE = b"GNU AFFERO GENERAL PUBLIC LICENSE\nCopyright (C) 2007 Free Software Foundation, Inc.\n"
SOURCE = b"# SPDX-License-Identifier: AGPL-3.0-only\n# Copyright Plastic Labs\nVALUE = 1\n"
README = b"# Honcho\n"


def _git(repo: Path, *args: str, stdin: str | None = None) -> str:
    return subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@example.invalid", *args], cwd=repo,
                          input=stdin, capture_output=True, text=True, check=True, timeout=60).stdout.strip()


def _notice(sha: str) -> str:
    return f"<!-- lina-modification-notice:start -->\n> Modified by LINA from `{sha}`.\n<!-- lina-modification-notice:end -->\n\n"


@pytest.fixture
def repo(tmp_path: Path) -> tuple[Path, str]:
    up = tmp_path / "upstream"
    (up / "src").mkdir(parents=True)
    (up / "LICENSE").write_bytes(LICENSE)
    (up / "src" / "app.py").write_bytes(SOURCE)
    (up / "README.md").write_bytes(README)
    _git(tmp_path, "init", "-q", str(up))
    _git(up, "add", ".")
    _git(up, "commit", "-qm", "upstream")
    upstream_sha = _git(up, "rev-parse", "HEAD")

    root = tmp_path / "lina"
    _git(tmp_path, "init", "-q", str(root))
    _git(root, "fetch", "-q", str(up), "HEAD")
    tree = _git(root, "rev-parse", f"{upstream_sha}^{{tree}}")
    squash = _git(root, "commit-tree", tree, "-m",
                  f"Squashed 'services/honcho/' content\n\ngit-subtree-dir: services/honcho\ngit-subtree-split: {upstream_sha}")
    _git(root, "reset", "-q", "--hard", squash)
    _git(root, "read-tree", "--prefix=services/honcho/", "-u", squash)
    (root / "services" / "honcho" / "README.md").write_text(_notice(upstream_sha) + README.decode())
    (root / "services" / "honcho-intake.md").write_text(
        f"# Ledger\n\n## Intakes\n\n| Date | Ref | Commit | Method |\n| --- | --- | --- | --- |\n"
        f"| 2026-11-02 | v1 | {upstream_sha} | add |\n\n## LINA changes\n\n| Path | Change | Since |\n")
    _git(root, "add", ".")
    _git(root, "commit", "-qm", "import")
    return root, upstream_sha


def _run(root: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run([sys.executable, str(SCRIPT), "--repo", str(root), *args], capture_output=True, text=True,
                          timeout=120, stdin=subprocess.DEVNULL, check=False)


def test_consistent_subtree_passes(repo):
    root, upstream_sha = repo
    result = _run(root, "--mirror", upstream_sha)
    assert result.returncode == 0, result.stdout + result.stderr


def test_changed_license_fails(repo):
    root, _ = repo
    (root / "services" / "honcho" / "LICENSE").write_bytes(LICENSE + b"extra\n")
    result = _run(root)
    assert result.returncode == 1
    assert "services/honcho/LICENSE: license file differs from upstream" in result.stdout


def test_removed_header_line_fails(repo):
    root, _ = repo
    (root / "services" / "honcho" / "src" / "app.py").write_bytes(b"# Copyright LINA\nVALUE = 1\n")
    result = _run(root)
    assert result.returncode == 1
    assert "services/honcho/src/app.py: upstream header line changed or removed" in result.stdout


def test_missing_notice_fails(repo):
    root, _ = repo
    (root / "services" / "honcho" / "README.md").write_bytes(README)
    result = _run(root)
    assert result.returncode == 1
    assert "must start with the LINA modification notice" in result.stdout


def test_ledger_not_matching_squash_fails(repo):
    root, upstream_sha = repo
    ledger = root / "services" / "honcho-intake.md"
    ledger.write_text(ledger.read_text().replace(upstream_sha, "f" * 40))
    result = _run(root)
    assert result.returncode == 1
    assert f"is of {upstream_sha}" in result.stdout
