"""scripts/ci/check_pruned_imports.py on real temp git repos: an upstream branch and a pruned dev."""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[2]
CHECK = REPO / "scripts" / "ci" / "check_pruned_imports.py"
sys.path.insert(0, str(CHECK.parent))

import check_pruned_imports as cpi


def _env() -> dict[str, str]:
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1", GIT_AUTHOR_NAME="t",
               GIT_AUTHOR_EMAIL="t@example.invalid", GIT_COMMITTER_NAME="t",
               GIT_COMMITTER_EMAIL="t@example.invalid")
    return env


def _git(repo: Path, *args: str) -> None:
    proc = subprocess.run(["git", *args], cwd=repo, env=_env(), capture_output=True, text=True,
                          stdin=subprocess.DEVNULL, timeout=120, check=False)
    assert proc.returncode == 0, (args, proc.stderr)


def _commit(repo: Path, files: dict[str, str | None]) -> None:
    for rel, text in files.items():
        if text is None:
            _git(repo, "rm", "-q", "-r", "--", rel)
            continue
        path = repo / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        _git(repo, "add", "--", rel)
    _git(repo, "commit", "-q", "-m", "step")


@pytest.fixture
def repo(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Upstream has gone/ and kept/; dev (checked out) deletes gone/ and kept/old.py."""
    monkeypatch.setattr(cpi, "git", lambda r, *a: _out(r, *a))
    _git(tmp_path, "init", "-q", "-b", "upstream")
    _commit(tmp_path, {"gone/__init__.py": "", "gone/mod.py": "X = 1\n",
                       "kept/__init__.py": "", "kept/a.py": "import os\n", "kept/old.py": "Y = 1\n"})
    _git(tmp_path, "checkout", "-q", "-b", "dev")
    _commit(tmp_path, {"gone": None, "kept/old.py": None})
    return tmp_path


def _out(repo: Path, *args: str) -> str:
    proc = subprocess.run(["git", *args], cwd=repo, env=_env(), capture_output=True, text=True,
                          stdin=subprocess.DEVNULL, timeout=120, check=False)
    if proc.returncode != 0:
        raise RuntimeError(proc.stderr.strip())
    return proc.stdout


def _check(repo: Path, capsys: pytest.CaptureFixture[str]) -> tuple[int, str]:
    code = cpi.main(["--repo", str(repo), "--upstream", "upstream"])
    return code, capsys.readouterr().out


def test_no_link_passes(repo, capsys):
    code, out = _check(repo, capsys)
    assert code == 0, out
    assert "links: 0 (excepted: 0)" in out


@pytest.mark.parametrize("source", [
    "import gone.mod\n",
    "from gone import mod\n",
    "def f():\n    from ..old import Y\n",
    "from .. import old\n",
    "import importlib\nM = importlib.import_module('gone.mod')\n",
    "M = __import__('gone')\n",
])
def test_link_into_deleted_module_fails(repo, capsys, source):
    _commit(repo, {"kept/sub/__init__.py": "", "kept/sub/b.py": source})
    code, out = _check(repo, capsys)
    assert code == 1, out
    assert "file=kept/sub/b.py" in out


def test_module_a_kept_file_still_provides_is_not_deleted(repo, capsys):
    _commit(repo, {"gone/kept_here.py": "", "kept/b.py": "import gone.kept_here\n"})
    code, out = _check(repo, capsys)
    assert code == 0, out


def test_excepted_file_is_listed_but_passes(repo, capsys, monkeypatch):
    _commit(repo, {"kept/b.py": "import gone.mod\n"})
    monkeypatch.setattr(cpi, "EXCEPTIONS", {"kept/b.py": "reason"})
    code, out = _check(repo, capsys)
    assert code == 0, out
    assert "links: 0 (excepted: 1)" in out
    assert "kept/b.py:1: gone.mod" in out


def test_unknown_upstream_ref_is_a_git_error(repo, capsys):
    code = cpi.main(["--repo", str(repo), "--upstream", "no-such-ref"])
    assert code == 2
