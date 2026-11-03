"""Checkpoint-backed restoration and inverse journal contracts."""

import hashlib
import json
import stat

import pytest

pytest_plugins = ["tests.hermes_cli.test_undo_support"]


@pytest.mark.parametrize("name", ["worker.bin", ".env", "archive.zip"])
def test_binary_restore_through_real_checkpoint_and_inverse(undo, name):
    # Given exact binary bytes, including filenames upstream normally excludes.
    path = undo.desk / name
    original = b"original\x00\xff\r\n"
    path.write_bytes(original)
    path.chmod(0o640)
    change = undo.begin(path)
    path.write_bytes(b"changed\n")
    undo.ok("end", change)
    # When applying the change and its inverse.
    undo.ok("apply", change)
    restored = path.read_bytes()
    mode = stat.S_IMODE(path.stat().st_mode)
    inverse = undo.journal()[-1]
    undo.ok("apply", inverse["change_id"])
    # Then both directions restore byte-identically with target permissions.
    assert restored == original
    assert mode == 0o640
    assert inverse["undo_of"] == change
    assert path.read_bytes() == b"changed\n"
    assert (undo.root / "checkpoints/store/HEAD").exists()


@pytest.mark.parametrize("created", [True, False])
def test_absent_file_restore_and_inverse(undo, created):
    # Given either a created or deleted file.
    path = undo.desk / "absent"
    if not created:
        path.write_bytes(b"old\r\n\xff")
    change = undo.begin(path)
    if created:
        path.write_bytes(b"new\r\n\xff")
    else:
        path.unlink()
    undo.ok("end", change)
    # When restoring its prior state.
    undo.ok("apply", change)
    restored = path.read_bytes() if path.exists() else None
    recreated_mode = stat.S_IMODE(path.stat().st_mode) if path.exists() else None
    undo.ok("apply", undo.journal()[-1]["change_id"])
    # Then absence is reversible and recreated files are private.
    assert restored == (None if created else b"old\r\n\xff")
    assert recreated_mode == (None if created else 0o600)
    assert (path.read_bytes() if path.exists() else None) == (b"new\r\n\xff" if created else None)


def test_stale_hash_preflight_prevents_partial_restore(undo):
    # Given two changed files and a later writer on the second.
    change = undo.begin(undo.memory, undo.rule)
    undo.memory.write_bytes(b"memory changed")
    undo.rule.write_bytes(b"rule changed")
    undo.ok("end", change)
    undo.rule.write_bytes(b"later writer")
    # When applying the batch.
    code, _, _ = undo.run("apply", change)
    # Then no first-file restore or inverse claims success.
    assert code == 2
    assert undo.memory.read_bytes() == b"memory changed"
    assert len(undo.journal()) == 1


def test_wrong_checkpoint_hash_refused(undo):
    # Given a valid checkpoint reference with a mismatched content hash.
    change = undo.begin(undo.memory)
    undo.memory.write_bytes(b"changed")
    undo.ok("end", change)
    row = undo.journal()[0]
    row["objects"][0]["sha256"] = hashlib.sha256(b"wrong").hexdigest()
    (undo.undo_root / "journal.jsonl").write_text(json.dumps(row) + "\n")
    # When applying it.
    code, _, _ = undo.run("apply", change)
    # Then the current file is preserved.
    assert code == 2 and undo.memory.read_bytes() == b"changed"


def test_consumed_entry_refuses_reapplication(undo):
    # Given a successfully applied file batch.
    change = undo.begin(undo.memory)
    undo.memory.write_bytes(b"changed")
    undo.ok("end", change)
    undo.ok("apply", change)
    # When applying the consumed original.
    code, _, _ = undo.run("apply", change)
    # Then only its inverse may be applied.
    assert code == 2 and len(undo.journal()) == 2


def test_symlink_target_refused(undo):
    # Given a changed file replaced with a link to another file.
    change = undo.begin(undo.memory)
    undo.memory.write_bytes(b"changed")
    undo.ok("end", change)
    undo.memory.unlink()
    undo.memory.symlink_to(undo.rule)
    # When applying it.
    code, _, _ = undo.run("apply", change)
    # Then the linked target remains untouched.
    assert code == 2 and undo.rule.read_bytes() == b"synthetic rule\n"


def test_mixed_batch_failure_records_partial_inverse(undo, monkeypatch):
    from hermes_cli.undo import objects
    from hermes_cli.undo.models import UndoError
    # Given a file followed by an external object command that fails.
    change = undo.begin(undo.memory)
    original = undo.memory.read_bytes()
    undo.memory.write_bytes(b"changed")
    undo.ok("end", change)
    ref = undo.journal()[0]["objects"][0]
    assert undo.log([ref, {"type": "ledger", "id": "T1", "rev": 1, "after": 2}], change_id="mixed")[0] == 0
    monkeypatch.setattr(objects, "check_ledger", lambda ref: None)

    def fail(ref):
        raise UndoError("synthetic command failure")

    monkeypatch.setattr(objects, "restore_ledger", fail)
    # When applying the mixed batch.
    code, _, _ = undo.run("apply", "mixed")
    # Then completed objects have a usable inverse.
    inverse = undo.journal()[-1]
    assert code == 2 and undo.memory.read_bytes() == original
    assert inverse["undo_of"] == "mixed" and len(inverse["objects"]) == 1
    undo.ok("apply", inverse["change_id"])
    assert undo.memory.read_bytes() == b"changed"
