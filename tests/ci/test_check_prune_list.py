"""prune-list.txt, scripts/ci/check_prune_list.py and scripts/sync-upstream on real temp git repos."""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[2]
CHECK = REPO / "scripts" / "ci" / "check_prune_list.py"
SYNC = REPO / "scripts" / "sync-upstream"
sys.path.insert(0, str(CHECK.parent))

import check_prune_list as cpl


def _env() -> dict[str, str]:
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1", GIT_AUTHOR_NAME="t",
               GIT_AUTHOR_EMAIL="t@example.invalid", GIT_COMMITTER_NAME="t",
               GIT_COMMITTER_EMAIL="t@example.invalid")
    return env


def _run(repo: Path, *args: str | Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run([str(a) for a in args], cwd=repo, env=_env(), capture_output=True,
                          text=True, encoding="utf-8", stdin=subprocess.DEVNULL, timeout=120,
                          check=False)


def _git(repo: Path, *args: str) -> str:
    proc = _run(repo, "git", *args)
    assert proc.returncode == 0, (args, proc.stderr)
    return proc.stdout.strip()


def _commit(repo: Path, files: dict[str, str | None], msg: str = "step") -> None:
    for rel, text in files.items():
        if text is None:
            _git(repo, "rm", "-q", "-r", "--", rel)
            continue
        path = repo / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        _git(repo, "add", "--", rel)
    _git(repo, "commit", "-q", "-m", msg)


def _tracked(repo: Path) -> set[str]:
    return set(_git(repo, "ls-files").splitlines())


def test_rules_match_prefixes_globs_and_keep_exceptions():
    rules = cpl.parse("# c\n\ndocker\ntests/test_x_*.py\nplugin-catalog/\n!plugin-catalog/keep.yaml\n")
    assert cpl.is_pruned(rules, "docker")
    assert cpl.is_pruned(rules, "docker/a/b.txt")
    assert not cpl.is_pruned(rules, "docker-compose.yml")
    assert not cpl.is_pruned(rules, "tools/docker")
    assert cpl.is_pruned(rules, "tests/test_x_one.py")
    assert not cpl.is_pruned(rules, "tests/test_y_one.py")
    assert cpl.is_pruned(rules, "plugin-catalog/other.yaml")
    assert not cpl.is_pruned(rules, "plugin-catalog/keep.yaml")


@pytest.mark.parametrize("line", ["/abs/path", "a/../b", "!", "win\\path"])
def test_a_line_that_is_not_a_repo_relative_path_is_an_error(line):
    with pytest.raises(cpl.PruneListError, match=":2:"):
        cpl.parse(f"ok\n{line}\n")


def test_the_shipped_list_never_prunes_its_own_machinery():
    rules = cpl.load(REPO)
    own = ["prune-list.txt", "scripts/sync-upstream", "scripts/ci/check_prune_list.py",
           "tests/ci/test_check_prune_list.py", ".github/workflows/ci.yml", "docs/forking.md"]
    assert [p for p in own if cpl.is_pruned(rules, p)] == []


def test_check_fails_while_a_tracked_file_matches_and_passes_once_removed(tmp_path):
    repo = tmp_path / "r"
    repo.mkdir()
    _git(repo, "init", "-q")
    _commit(repo, {"prune-list.txt": "docker\n", "docker/a.txt": "a", "keep.py": "k"})

    failing = _run(repo, sys.executable, CHECK, "--repo", repo)
    assert failing.returncode == 1, failing.stdout + failing.stderr
    assert "docker/a.txt" in failing.stdout

    _commit(repo, {"docker": None})
    passing = _run(repo, sys.executable, CHECK, "--repo", repo)
    assert passing.returncode == 0, passing.stdout + passing.stderr
    assert _run(repo, sys.executable, CHECK, "--repo", repo, "--rev", "HEAD~1").returncode == 1


def _fork(tmp_path: Path) -> Path:
    """An upstream history and a LINA branch from it that already pruned ``docker``."""
    repo = tmp_path / "lina"
    repo.mkdir()
    _git(repo, "init", "-q", "-b", "upstream")
    _commit(repo, {"keep.py": "base\n", "docker/a.txt": "a\n",
                   "plugins/platforms/telegram/x.py": "t\n"}, "upstream base")
    _git(repo, "switch", "-q", "-c", "dev")
    lists = "docker\nplugins/platforms/matrix\n"
    files: dict[str, str | None] = {"prune-list.txt": lists, "docker": None}
    for src in (CHECK, SYNC):
        files[str(src.relative_to(REPO))] = src.read_text(encoding="utf-8")
    _commit(repo, files, "lina: prune")
    (repo / "scripts" / "sync-upstream").chmod(0o755)
    _git(repo, "add", "scripts/sync-upstream")
    _git(repo, "commit", "-q", "--amend", "--no-edit")
    _git(repo, "switch", "-q", "upstream")
    _commit(repo, {"docker/a.txt": "changed upstream\n", "docker/b.txt": "new under pruned\n",
                   "plugins/platforms/matrix/m.py": "m\n", "plugins/platforms/newp/n.py": "n\n",
                   "newtop/f.txt": "f\n", "keep.py": "base\nupstream line\n"}, "upstream moves")
    _git(repo, "switch", "-q", "dev")
    _git(repo, "switch", "-q", "-c", "chore/take-upstream")
    return repo


def test_sync_merges_removes_the_prune_set_and_reports_new_paths(tmp_path):
    repo = _fork(tmp_path)
    proc = _run(repo, sys.executable, repo / "scripts" / "sync-upstream", "upstream")
    assert proc.returncode == 0, proc.stdout + proc.stderr

    tracked = _tracked(repo)
    assert {"docker/a.txt", "docker/b.txt", "plugins/platforms/matrix/m.py"}.isdisjoint(tracked)
    assert {"keep.py", "newtop/f.txt", "plugins/platforms/newp/n.py"} <= tracked
    assert (repo / "keep.py").read_text(encoding="utf-8") == "base\nupstream line\n"
    assert _git(repo, "diff", "--name-only", "--diff-filter=U") == ""
    assert _git(repo, "rev-parse", "MERGE_HEAD") == _git(repo, "rev-parse", "upstream")
    report = proc.stdout.split("or keep it:", 1)[1].split("Next:", 1)[0]
    assert report.split() == ["newtop", "plugins/platforms/newp"]

    _git(repo, "commit", "-q", "--no-edit")
    assert _run(repo, sys.executable, repo / "scripts/ci/check_prune_list.py",
                "--repo", repo).returncode == 0


def test_sync_lists_conflicts_left_in_kept_files(tmp_path):
    repo = _fork(tmp_path)
    _commit(repo, {"keep.py": "base\nlina line\n"}, "lina edits keep.py")
    proc = _run(repo, sys.executable, repo / "scripts" / "sync-upstream", "upstream")
    assert proc.returncode == 1, proc.stdout + proc.stderr
    assert _git(repo, "diff", "--name-only", "--diff-filter=U") == "keep.py"
    assert "    keep.py" in proc.stdout.split("Conflicts left in kept files", 1)[1]
    assert "docker/a.txt" not in _tracked(repo)


def test_sync_refuses_to_run_on_dev(tmp_path):
    repo = _fork(tmp_path)
    _git(repo, "switch", "-q", "dev")
    proc = _run(repo, sys.executable, repo / "scripts" / "sync-upstream", "upstream")
    assert proc.returncode == 2
    assert "refused" in proc.stderr
    assert _run(repo, "git", "rev-parse", "-q", "--verify", "MERGE_HEAD").returncode != 0


def test_sync_with_nothing_new_changes_nothing(tmp_path):
    repo = _fork(tmp_path)
    head = _git(repo, "rev-parse", "HEAD")
    proc = _run(repo, sys.executable, repo / "scripts" / "sync-upstream", "upstream~1")
    assert proc.returncode == 0, proc.stderr
    assert "already merged" in proc.stdout
    assert _git(repo, "rev-parse", "HEAD") == head
