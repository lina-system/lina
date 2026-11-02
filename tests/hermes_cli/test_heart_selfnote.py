"""Checked self-note edit, review and undo behavior."""

import json
from pathlib import Path

import pytest

from hermes_cli.heart import selfnote, storage
from hermes_cli.heart.models import Change, Entry
from hermes_cli.work import env
from tests.hermes_cli.test_heart_journal import DAY, NOW, STAMP, database, invoke, records, sandbox, write

__all__ = ["sandbox"]


@pytest.fixture
def note(sandbox):
    database(sandbox["alpha"])
    write()
    soul = sandbox["alpha"] / "SOUL.self.md"
    soul.write_text(f"Original tone ({DAY}, evidence {STAMP})\n")
    return soul


def apply(soul, value):
    candidate = soul.parent / "candidate.md"
    candidate.write_text(value)
    return invoke("self-apply", "--file", str(candidate))


@pytest.mark.parametrize(("value", "rule"), [
    ("x" * 1623, "length"),
    ("New without citation\n", "evidence-suffix"),
    (f"New ({DAY}, evidence 1999-01-01T00:00:00+00:00)\n", "evidence-ts"),
    (f"[IMPORTANT] New ({DAY}, evidence {STAMP})\n", "machine-prefix"),
    ("x" * 201, "line-length"),
    ("\n".join(f"New {i} ({DAY}, evidence {STAMP})" for i in range(3)), "line-count"),
])
def test_refusal_leaves_note_and_records_rule(note, value, rule):
    # Given
    before = note.read_bytes()
    # When
    code, output, _ = apply(note, value)
    # Then
    assert code == 1
    assert rule in output
    assert note.read_bytes() == before
    assert records(note.parent / "heart/changes.jsonl")[-1]["refused"] == rule
    assert not (note.parent / "heart/self-backups").exists()


def test_length_reports_actual_overflow(note):
    result = apply(note, "x" * 1623)
    assert all(number in result[1] for number in ("1623", "1500", "123"))


def test_future_journal_timestamp_is_refused(note):
    path = note.parent / "heart/journal.jsonl"
    future = storage.stamp(NOW + 1)
    storage.append(path, Entry.model_validate({**records(path)[0], "ts": future}))
    assert apply(note, f"New ({DAY}, evidence {future})\n")[0] == 1
    assert records(note.parent / "heart/changes.jsonl")[-1]["refused"] == "evidence-ts"


@pytest.mark.parametrize("topic", ["reporting", "honesty", "permissions", "verification", "보고", "정직", "권한", "검증"])
@pytest.mark.parametrize("removed", [False, True])
def test_protected_topics_refused_on_addition_and_removal(note, topic, removed):
    value = f"Change {topic} ({DAY}, evidence {STAMP})\n"
    if removed:
        note.write_text(value)
        value = ""
    before = note.read_bytes()
    result = apply(note, value)
    assert result[0] == 1 and "protected-topic" in result[1]
    assert note.read_bytes() == before


def test_accept_creates_private_backup_and_audit(note):
    before = note.read_text()
    new = before + f"Gentler greeting ({DAY}, evidence {STAMP})\n"
    assert apply(note, new)[0] == 0
    assert note.read_text() == new
    backup = next((note.parent / "heart/self-backups").glob("*.md"))
    assert backup.read_text() == before
    assert backup.stat().st_mode & 0o777 == 0o600
    assert note.stat().st_mode & 0o777 == 0o600
    change = records(note.parent / "heart/changes.jsonl")[-1]
    assert change["added"] == [f"Gentler greeting ({DAY}, evidence {STAMP})"]
    assert change["evidence"] == [STAMP]


def test_undo_restores_latest_same_second_backup(note):
    original = note.read_text()
    second = original + f"Second tone ({DAY}, evidence {STAMP})\n"
    third = second + f"Third tone ({DAY}, evidence {STAMP})\n"
    assert apply(note, second)[0] == 0
    assert apply(note, third)[0] == 0
    assert invoke("undo")[0] == 0
    assert note.read_text() == second
    assert records(note.parent / "heart/changes.jsonl")[-1]["undo"].endswith("-2.md")


def test_removal_needs_no_evidence_but_counts_toward_limit(note):
    note.write_text("Old (2020-01-01, initial)\n")
    assert apply(note, "")[0] == 0
    assert note.read_text() == ""


def test_three_removed_lines_are_refused(note):
    note.write_text("one\ntwo\nthree\n")
    assert apply(note, "")[0] == 1
    assert records(note.parent / "heart/changes.jsonl")[-1]["refused"] == "line-count"


def test_unchanged_records_zero_without_backup(note):
    assert apply(note, note.read_text())[0] == 0
    assert records(note.parent / "heart/changes.jsonl")[-1] == {"ts": STAMP, "changed": 0}
    assert not (note.parent / "heart/self-backups").exists()


def test_undo_without_backup_preserves_note(note):
    before = note.read_bytes()
    assert invoke("undo")[0] == 1
    assert note.read_bytes() == before


def test_review_includes_day_note_and_last_five_changes(note):
    path = note.parent / "heart/changes.jsonl"
    for index in range(7):
        storage.append(path, Change(ts=storage.stamp(NOW - index), changed=0))
    code, output, _ = invoke("review-input", "--date", DAY)
    assert code == 0
    assert STAMP + "  " in output
    assert note.read_text().strip() in output
    changes = [json.loads(line) for line in output.splitlines() if line.startswith("{")]
    assert [row["ts"] for row in changes] == [storage.stamp(NOW - i) for i in range(2, 7)]


def test_removal_candidates_oldest_only_above_soft_limit(note):
    lines = [f"{index} " + "x" * 170 + f" (2020-01-0{index}, initial)" for index in range(1, 9)]
    value = "\n".join(lines) + "\n"
    selected = selfnote.removal_candidates(value)
    assert selected == lines[:2]
    assert len("\n".join(line for line in lines if line not in selected) + "\n") < 1300
    assert selfnote.removal_candidates(lines[0]) == []


def test_korean_suffix_supported_with_korean_output(note):
    env.current().locale = "ko"
    assert apply(note, f"부드러운 말투 ({DAY}, 근거 {STAMP})\n")[0] == 0


def test_backup_order_handles_offset_and_collision_numbers():
    assert selfnote.backup_key(Path("2026-10-26T120000-0400-2.md")) > selfnote.backup_key(Path("2026-10-26T120000-0400.md"))
    assert selfnote.backup_key(Path("2026-10-27T120000+0000.md")) > selfnote.backup_key(Path("2026-10-26T120000+0000-2.md"))
