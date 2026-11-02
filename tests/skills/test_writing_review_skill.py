"""writing-review's words-check.sh keeps the exit code contract its SKILL.md states.

The skill tells the agent: ``0 clean, 1 warnings, 2 invocation or read failure``. The agent decides
whether to revise a draft from that code alone, so each branch is run against real files.
"""

from __future__ import annotations

import re
import shutil
import subprocess
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[2]
SKILL = REPO / "skills" / "productivity" / "writing-review"
SCRIPT = SKILL / "scripts" / "words-check.sh"

pytestmark = [
    pytest.mark.platforms("posix"),
    pytest.mark.skipif(shutil.which("bash") is None, reason="the checker is a bash script"),
]


def _check(*args: str | Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(["bash", str(SCRIPT), *map(str, args)], capture_output=True, text=True,
                          encoding="utf-8", check=False, timeout=30)


def _draft(tmp_path: Path, name: str, text: str) -> Path:
    path = tmp_path / name
    path.write_text(text, encoding="utf-8")
    return path


def test_documented_exit_codes_match_the_script():
    body = (SKILL / "SKILL.md").read_text(encoding="utf-8")
    assert re.search(r"0 clean, 1 warnings, 2 invocation or read failure", body)


def test_clean_draft_exits_zero_and_prints_nothing(tmp_path):
    result = _check(_draft(tmp_path, "clean.md", "The build passed on the first try.\n"))
    assert (result.returncode, result.stdout) == (0, "")


def test_warning_is_reported_with_its_line_case_insensitively(tmp_path):
    draft = _draft(tmp_path, "draft.md", "First line.\nIt Is Worth Noting that it shipped.\n")
    result = _check(draft)
    assert result.returncode == 1
    assert "2:It Is Worth Noting that it shipped." in result.stdout
    assert "Style warnings found" in result.stderr


def test_several_files_name_the_file_of_each_match(tmp_path):
    clean = _draft(tmp_path, "clean.md", "Plain words.\n")
    noisy = _draft(tmp_path, "noisy.md", "We delve into it.\n")
    result = _check(clean, noisy)
    assert result.returncode == 1
    assert f"{noisy}:1:We delve into it." in result.stdout
    assert str(clean) not in result.stdout


def test_several_clean_files_exit_zero(tmp_path):
    files = [_draft(tmp_path, f"{n}.md", "Plain words.\n") for n in range(3)]
    assert _check(*files).returncode == 0


def test_no_arguments_is_an_invocation_failure():
    result = _check()
    assert result.returncode == 2
    assert "Usage: words-check.sh FILE..." in result.stderr


def test_missing_file_is_a_read_failure_not_clean(tmp_path):
    assert _check(tmp_path / "absent.md").returncode == 2


def test_read_failure_wins_over_a_match_in_another_file(tmp_path):
    noisy = _draft(tmp_path, "noisy.md", "We delve into it.\n")
    assert _check(noisy, tmp_path / "absent.md").returncode == 2


def test_runs_from_any_working_directory(tmp_path):
    draft = _draft(tmp_path, "draft.md", "In conclusion, done.\n")
    result = subprocess.run(["bash", str(SCRIPT), draft.name], cwd=tmp_path, capture_output=True,
                            text=True, encoding="utf-8", check=False, timeout=30)
    assert result.returncode == 1
