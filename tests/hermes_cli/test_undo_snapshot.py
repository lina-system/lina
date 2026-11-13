"""``lina undo snapshot``: the daily copy journals files changed outside begin/end (lina#484)."""

import hashlib

pytest_plugins = ["tests.hermes_cli.test_undo_support"]


def test_first_snapshot_records_copies_then_changes_journal_and_apply_restores(undo):
    # Given a first snapshot of the synthetic memory and rule files.
    original = undo.memory.read_bytes()
    first = undo.ok("snapshot")
    assert "first copy" in first and undo.journal() == []
    # When both change outside begin/end and the next snapshot runs.
    undo.memory.write_bytes(b"memory tool rewrite")
    undo.rule.write_bytes(b"hand edit")
    undo.ok("snapshot")
    # Then each owner gets one revert entry pointing at the earlier copy.
    rows = {row["bot"]: row for row in undo.journal()}
    assert set(rows) == {"alpha", "lina-rules"}
    ref = rows["alpha"]["objects"][0]
    assert (ref["path"], ref["type"], ref["sha256"]) == (
        str(undo.memory), "memory", hashlib.sha256(original).hexdigest())
    # When applying the alpha entry.
    undo.ok("apply", rows["alpha"]["change_id"])
    # Then the memory file is back to the earlier copy.
    assert undo.memory.read_bytes() == original


def test_unchanged_snapshot_journals_nothing(undo):
    # Given a recorded snapshot.
    undo.ok("snapshot")
    # When nothing changed before the next one.
    output = undo.ok("snapshot")
    # Then no entry is journaled.
    assert output.strip() == "No changed files since the last snapshot"
    assert undo.journal() == []


def test_dry_run_lists_files_and_writes_nothing(undo):
    # Given no snapshot yet; when running it dry.
    output = undo.ok("snapshot", "--dry-run")
    # Then it names the files and leaves the undo store untouched.
    assert f"would record the first copy of alpha: {undo.memory}" in output
    assert f"would record the first copy of lina-rules: {undo.rule}" in output
    assert not undo.undo_root.exists()
