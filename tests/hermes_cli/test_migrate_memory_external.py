"""Copied-spool migration continues the real source's owner-side cursor."""

from hermes_cli.external_messages import ingest
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
