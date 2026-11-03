"""Copied-spool migration continues the real source's owner-side cursor."""

import pytest

from hermes_cli.external_messages import SpoolError, ingest
from hermes_cli.migrate_memory_external import migrate_external
from tests.hermes_cli.test_external_messages import event, message


def test_copied_spool_cursor_continues_at_live_path(tmp_path, memory_store_sql):
    original = tmp_path / "live.jsonl"
    original.write_bytes(event(message()) + event(message(text="second version"), number=2))
    copy = tmp_path / "copy.jsonl"
    copy.write_bytes(original.read_bytes())
    before = original.read_bytes()

    first = migrate_external(copy, original)

    assert first.ok and first.inserted == 2
    assert original.read_bytes() == before
    assert memory_store_sql("SELECT spool, byte_offset FROM memory.external_message_cursors") == [
        (str(original), len(before))]
    assert migrate_external(copy, original).inserted == 0
    with original.open("ab") as stream:
        stream.write(event(message(native="next"), number=3))
    assert ingest(original).inserted == 1


def test_snapshot_rerun_after_live_versions_preserves_cursor_and_heads(tmp_path, memory_store_sql):
    # Given: the live collector has moved beyond a snapshot, including a new version of its UID.
    original = tmp_path / "live.jsonl"
    original.write_bytes(event(message()) + event(message(), number=2))
    copy = tmp_path / "copy.jsonl"
    copy.write_bytes(original.read_bytes())
    migrate_external(copy, original)
    with original.open("ab") as stream:
        stream.write(event(message(text="later live version"), message(native="next"), number=3))
    ingest(original)
    cursor = memory_store_sql("SELECT * FROM memory.external_message_cursors")
    heads = memory_store_sql("SELECT * FROM memory.external_message_heads ORDER BY uid")

    # When: the unchanged migration snapshot is replayed.
    report = migrate_external(copy, original)

    # Then: only copied occurrences are verified and live state is untouched.
    assert report.ok and (report.inserted, report.matched) == (0, 2)
    assert memory_store_sql("SELECT * FROM memory.external_message_cursors") == cursor
    assert memory_store_sql("SELECT * FROM memory.external_message_heads ORDER BY uid") == heads


def test_advanced_cursor_refuses_changed_snapshot_prefix(tmp_path, memory_store_sql):
    # Given: all payloads exist, but the snapshot event bytes differ from the consumed prefix.
    original = tmp_path / "live.jsonl"
    original.write_bytes(event(message()))
    copy = tmp_path / "copy.jsonl"
    copy.write_bytes(original.read_bytes())
    migrate_external(copy, original)
    with original.open("ab") as stream:
        stream.write(event(message(native="next"), number=2))
    ingest(original)
    copy.write_bytes(event(message(), number=9))
    cursor = memory_store_sql("SELECT * FROM memory.external_message_cursors")

    # When / Then: byte-prefix verification refuses without replacing the newer cursor.
    with pytest.raises(SpoolError, match="prefix"):
        migrate_external(copy, original)
    assert memory_store_sql("SELECT * FROM memory.external_message_cursors") == cursor


@pytest.mark.parametrize("change", ["missing", "corrupt"])
def test_advanced_cursor_does_not_hide_damaged_copied_version(tmp_path, memory_store_sql, change):
    # Given: the cursor advanced normally, but a copied immutable message was lost or changed.
    original = tmp_path / "live.jsonl"
    original.write_bytes(event(message()))
    copy = tmp_path / "copy.jsonl"
    copy.write_bytes(original.read_bytes())
    migrate_external(copy, original)
    with original.open("ab") as stream:
        stream.write(event(message(native="next"), number=2))
    ingest(original)
    uid = "messages:" + message()["uid"]
    if change == "missing":
        memory_store_sql("DELETE FROM memory.messages WHERE uid=%s", (uid,))
    else:
        memory_store_sql("UPDATE memory.messages SET content='corrupt copy' WHERE uid=%s", (uid,))
    cursor = memory_store_sql("SELECT * FROM memory.external_message_cursors")

    # When: the old copy is verified without reingesting it.
    report = migrate_external(copy, original)

    # Then: the failure cannot be concealed by the newer cursor or repaired by replay.
    assert not report.ok and report.inserted == 0 and len(report.as_dict()["missing"]) == 1
    assert memory_store_sql("SELECT * FROM memory.external_message_cursors") == cursor


def test_missing_destination_version_is_not_hidden_by_cursor(tmp_path, memory_store_sql):
    copy = tmp_path / "copy.jsonl"
    copy.write_bytes(event(message()))
    migrate_external(copy, tmp_path / "live.jsonl")
    memory_store_sql("DELETE FROM memory.messages WHERE source='messages'")

    report = migrate_external(copy, tmp_path / "live.jsonl")

    assert not report.ok
    assert len(report.as_dict()["missing"]) == 1


def test_repeated_source_occurrences_are_counted_as_matched(tmp_path, memory_store_sql):
    copy = tmp_path / "copy.jsonl"
    copy.write_bytes(event(message()) + event(message(), number=2))

    report = migrate_external(copy, tmp_path / "live.jsonl")

    assert report.ok
    assert len(report.source) == 2
    assert (report.matched, report.inserted) == (1, 1)
