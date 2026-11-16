"""lina#467: ``lina memory profile merge`` on a fixture install (never a real profile directory)."""

import os
import subprocess
import sys
from pathlib import Path

import pytest

from tools.memory_tool_store import ENTRY_DELIMITER

REPO = Path(__file__).resolve().parents[2]
PROFILES = {
    "a": ["Prefers short answers", "Ask before deleting files", "Timezone is UTC+9"],
    "b": ["Prefers short answers", "ask before  deleting files.", "Likes green tea"],
    "c": ["Prefers short answers", "Timezone is UTC+1"],
}


@pytest.fixture()
def install(tmp_path):
    root = tmp_path / "home" / ".lina"
    (root / "profiles").mkdir(parents=True)
    (root / "config.yaml").write_text("memory:\n  user_char_limit: 2500\n")
    for name, entries in PROFILES.items():
        home = root / "profiles" / name
        (home / "memories").mkdir(parents=True)
        (home / "config.yaml").write_text("memory:\n  user_char_limit: 2500\n")
        (home / "memories" / "USER.md").write_text(ENTRY_DELIMITER.join(entries))
    return root


def _user_md(root: Path, name: str) -> Path:
    return root / "profiles" / name / "memories" / "USER.md"


def _merge(root: Path, *args: str, answer: str = ""):
    env = dict(os.environ, HOME=str(root.parent), HERMES_HOME=str(root), LINA_HOME=str(root), PYTHONPATH=str(REPO))
    return subprocess.run([sys.executable, "-m", "hermes_cli.main", "memory", "profile", "merge", *args], cwd=REPO,
                          env=env, input=answer, capture_output=True, text=True, timeout=180, check=False)


def _snapshot(root: Path) -> dict[str, bytes]:
    """Every memory file's bytes (lock files aside: taking the lock is not a change)."""
    return {str(p.relative_to(root)): p.read_bytes() for p in sorted(root.rglob("*"))
            if p.is_file() and p.suffix != ".lock" and ("memories" in p.parts or p.parent.name == "shared")}


def test_dry_run_lists_moves_diverged_and_budgets_and_writes_nothing(install):
    before = _snapshot(install)
    result = _merge(install)
    assert result.returncode == 0, result.stderr
    out = result.stdout
    assert "[identical] Prefers short answers" in out
    assert "[near-identical] Ask before deleting files" in out
    assert "a: Timezone is UTC+9" in out and "c: Timezone is UTC+1" in out
    assert "shared owner profile (shared/OWNER.md): 0 -> " in out and "/ 2,500 chars" in out
    assert "Likes green tea" not in out
    assert _snapshot(install) == before and not (install / "shared" / "OWNER.md").exists()


def test_apply_without_the_typed_confirmation_changes_nothing(install):
    before = _snapshot(install)
    result = _merge(install, "--apply", answer="yes\n")
    assert result.returncode == 1 and "Not confirmed" in result.stdout
    assert _snapshot(install) == before


def test_apply_moves_identical_and_near_identical_keeps_diverged_and_backs_up(install):
    originals = {name: _user_md(install, name).read_bytes() for name in PROFILES}
    result = _merge(install, "--apply", answer="merge\n")
    assert result.returncode == 0, result.stdout + result.stderr
    shared = (install / "shared" / "OWNER.md").read_text().split(ENTRY_DELIMITER)
    assert shared == ["Prefers short answers", "Ask before deleting files"]
    remaining = {name: _user_md(install, name).read_text().split(ENTRY_DELIMITER) for name in PROFILES}
    assert remaining == {"a": ["Timezone is UTC+9"], "b": ["Likes green tea"], "c": ["Timezone is UTC+1"]}
    for name, raw in originals.items():
        backups = list(_user_md(install, name).parent.glob("USER.md.bak-*"))
        assert [b.read_bytes() for b in backups] == [raw]
    again = _merge(install)
    assert "Nothing to move." in again.stdout and "Timezone is UTC+9" in again.stdout


def test_apply_refuses_when_the_shared_layer_would_be_full(install):
    (install / "config.yaml").write_text("memory:\n  shared_user_char_limit: 30\n")
    before = _snapshot(install)
    result = _merge(install, "--apply", answer="merge\n")
    assert result.returncode == 1
    assert "The shared owner profile (shared/OWNER.md) is full" in result.stdout
    assert _snapshot(install) == before


def _plan_and_apply(root: Path, monkeypatch, between=None):
    """Build the plan, let *between* change files (the owner reading the prompt), then apply."""
    from hermes_cli import memory_profile_merge as merge
    monkeypatch.setenv("HOME", str(root.parent))
    monkeypatch.setenv("HERMES_HOME", str(root))
    monkeypatch.setenv("LINA_HOME", str(root))
    profiles = [(name, root / "profiles" / name) for name in sorted(p.name for p in (root / "profiles").iterdir())]
    plan = merge.build_plan(profiles, root / "shared" / "OWNER.md")
    if between:
        between()
    return merge.apply(plan)


def test_a_shared_entry_corrected_after_the_plan_is_not_joined_by_the_old_wording(install, monkeypatch):
    shared = install / "shared" / "OWNER.md"
    shared.parent.mkdir()
    shared.write_text("Prefers short answers!")  # near-identical to the profiles' wording: a planned move

    def corrected():
        shared.write_text("Prefers long answers")

    ok, lines = _plan_and_apply(install, monkeypatch, corrected)
    assert ok is False and any("shared layer changed since the plan" in line for line in lines)
    text = shared.read_text()
    assert "Prefers long answers" in text and "Prefers short answers" not in text  # the old wording never joins it
    for name in "abc":  # nothing moved out of the profiles
        assert "Prefers short answers" in _user_md(install, name).read_text()


def test_a_fact_added_to_the_shared_layer_after_the_plan_stops_that_move_only(install, monkeypatch):
    shared = install / "shared" / "OWNER.md"

    def added():
        shared.parent.mkdir(exist_ok=True)
        shared.write_text("Prefers short answer")  # a new entry, near the planned fact but not equal

    ok, _lines = _plan_and_apply(install, monkeypatch, added)
    assert ok is False
    assert "Ask before deleting files" in shared.read_text()  # the other planned move went through
    assert "Prefers short answers" not in shared.read_text()
    for name in "abc":
        assert "Prefers short answers" in _user_md(install, name).read_text()


def test_a_profile_named_shared_is_not_taken_for_the_shared_layer(install, monkeypatch):
    home = install / "profiles" / "shared"
    (home / "memories").mkdir(parents=True)
    (home / "config.yaml").write_text("memory:\n  user_char_limit: 2500\n")
    (home / "memories" / "USER.md").write_text(ENTRY_DELIMITER.join(["Prefers short answers", "Only here"]))
    ok, lines = _plan_and_apply(install, monkeypatch)
    assert ok is True, lines
    assert "Prefers short answers" not in _user_md(install, "shared").read_text()  # moved like any profile's
    assert "Only here" in _user_md(install, "shared").read_text()
    assert "Prefers short answers" in (install / "shared" / "OWNER.md").read_text()
