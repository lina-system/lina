"""Pending batch contracts ported from the owner's begin tests."""

import json
import stat
from concurrent.futures import ThreadPoolExecutor

import pytest

pytest_plugins = ["tests.hermes_cli.test_undo_support"]


def test_begin_private_metadata_and_checkpoint_reference(undo):
    # Given a binary profile memory.
    original = undo.memory.read_bytes()
    # When opening a duplicate-path batch.
    change = undo.begin(undo.memory, undo.memory)
    # Then only references enter the private pending receipt.
    row = json.loads(undo.pending(change).read_text())
    assert len(row["objects"]) == 1
    assert row["objects"][0]["checkpoint"]
    assert original not in undo.pending(change).read_bytes()
    assert undo.journal() == []
    for path in [undo.undo_root, *undo.undo_root.rglob("*")]:
        assert stat.S_IMODE(path.stat().st_mode) == (0o700 if path.is_dir() else 0o600)


def test_end_classifies_absent_paths_and_omits_unchanged(undo):
    # Given memory, rule, new rule and ordinary absent files.
    new_rule = undo.desk / "new-rule.md"
    desk = undo.desk / "worker.bin"
    unchanged = undo.desk / "untouched"
    unchanged.write_bytes(b"same")
    change = undo.begin(undo.memory, undo.rule, new_rule, desk, unchanged)
    undo.memory.unlink()
    for path in (undo.rule, new_rule, desk):
        path.write_bytes(b"edited\r\n\xff")
    # When closing the batch.
    undo.ok("end", change)
    # Then typing was determined before the files existed.
    refs = {r["path"]: r for r in undo.journal()[0]["objects"]}
    assert {p: r["type"] for p, r in refs.items()} == {
        str(undo.memory): "memory", str(undo.rule): "rule", str(new_rule): "rule", str(desk): "file"}
    assert not undo.pending(change).exists()


def test_unchanged_batch_closes_without_journal(undo):
    # Given an unchanged existing and absent file.
    change = undo.begin(undo.memory, undo.desk / "missing")
    # When closing it.
    undo.ok("end", change)
    # Then no change is journaled.
    assert undo.journal() == []
    assert not undo.pending(change).exists()


def test_pending_masking_and_relative_path_resolution(undo, monkeypatch):
    # Given a relative file and secret-bearing description.
    monkeypatch.chdir(undo.desk)
    # When beginning a batch.
    change = undo.begin("rule.md", what="secret=synthetic-only")
    # Then the reference is absolute and description is masked.
    row = json.loads(undo.pending(change).read_text())
    assert row["objects"][0]["path"] == str(undo.rule)
    assert "synthetic-only" not in row["what"]


def test_last_unfinished_is_bot_scoped_and_independent_of_within(undo):
    # Given old, other-bot and fresh batches.
    old = undo.begin(undo.memory)
    other = undo.begin(undo.rule, bot="beta")
    undo.clock[0] += 86401
    fresh = undo.begin(undo.rule)
    # When asking for a one-minute window.
    output = undo.ok("last", "--bot", "alpha", "--within", "1m")
    # Then only the old selected-bot batch is unfinished.
    assert old in output and "unfinished" in output
    assert other not in output and fresh not in output


def test_end_symlink_failure_preserves_pending_for_retry(undo):
    # Given a captured file replaced with a symlink.
    change = undo.begin(undo.memory)
    undo.memory.unlink()
    undo.memory.symlink_to(undo.rule)
    # When closing it.
    code, _, _ = undo.run("end", change)
    # Then it remains pending without a journal append.
    assert code == 2
    assert undo.pending(change).exists() and undo.journal() == []


def test_end_retry_after_append_does_not_duplicate_or_rehash(undo):
    # Given an append that succeeded before pending cleanup.
    change = undo.begin(undo.memory)
    receipt = undo.pending(change).read_bytes()
    undo.memory.write_bytes(b"changed")
    undo.ok("end", change)
    expected = undo.journal()[0]
    undo.pending(change).write_bytes(receipt)
    undo.memory.write_bytes(b"later writer")
    # When retrying close.
    undo.ok("end", change)
    # Then the original after hash remains authoritative.
    assert undo.journal() == [expected]


@pytest.mark.parametrize("change", ["../journal", "../../outside", "/absolute"])
def test_end_rejects_pending_traversal(undo, change):
    # Given an invalid pending identifier; when closing it.
    code, _, _ = undo.run("end", change)
    # Then traversal is refused.
    assert code == 2


def test_end_corrupt_journal_keeps_pending(undo):
    # Given a pending change and incomplete journal.
    change = undo.begin(undo.memory)
    undo.memory.write_bytes(b"changed")
    (undo.undo_root / "journal.jsonl").write_text('{"incomplete":')
    # When closing it.
    code, _, _ = undo.run("end", change)
    # Then the batch remains retryable.
    assert code == 2 and undo.pending(change).exists()


def test_concurrent_end_appends_once(undo):
    from hermes_cli.undo.pending import end
    from hermes_cli.undo.storage import Store
    # Given one completed edit with two cooperating closers.
    change = undo.begin(undo.memory)
    undo.memory.write_bytes(b"changed")

    def close():
        from hermes_cli.undo.models import UndoError
        try:
            return end(Store(), change)
        except UndoError:
            return None

    # When both close without timing sleeps.
    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(lambda _: close(), range(2)))
    # Then exactly one entry exists.
    assert change in results
    assert len(undo.journal()) == 1
