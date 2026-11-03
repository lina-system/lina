"""The host source switch preserves the real store and spool cursor (lina#23)."""

from hermes_cli.config import atomic_config_write
from hermes_cli.external_messages import ingest
from tests.hermes_cli.test_external_messages import event, message
from utils import atomic_roundtrip_yaml_update


def test_off_preserves_cursor_and_on_consumes_pending_lines_once(tmp_path, monkeypatch, memory_store_sql):
    from cron.external_messages import maybe_ingest
    import hermes_constants

    # Given one consumed event and another appended while the source is off.
    monkeypatch.setattr(hermes_constants, "get_default_hermes_root", lambda: tmp_path)
    spool = tmp_path / "events.jsonl"
    spool.write_bytes(event(message()))
    config = tmp_path / "config.yaml"
    atomic_config_write(config, {"memory": {"external_messages": {"spool": str(spool)}}})
    assert ingest(spool).inserted == 1
    rows = memory_store_sql("SELECT * FROM memory.messages ORDER BY message_id")
    cursor = memory_store_sql("SELECT * FROM memory.external_message_cursors")
    heads = memory_store_sql("SELECT * FROM memory.external_message_heads")
    atomic_roundtrip_yaml_update(config, "memory.external_messages.enabled", False)
    with spool.open("ab") as stream:
        stream.write(event(message(native="2"), number=2))
    pending = spool.read_bytes()

    # When both scheduled and explicit ingestion run while off.
    maybe_ingest()
    assert ingest(spool).inserted == 0

    # Then no persisted state changes; enabling resumes the saved cursor exactly once.
    assert memory_store_sql("SELECT * FROM memory.messages ORDER BY message_id") == rows
    assert memory_store_sql("SELECT * FROM memory.external_message_cursors") == cursor
    assert memory_store_sql("SELECT * FROM memory.external_message_heads") == heads
    assert spool.read_bytes() == pending
    assert not (tmp_path / "external_messages" / "ticker.json").exists()
    atomic_roundtrip_yaml_update(config, "memory.external_messages.enabled", True)
    maybe_ingest()
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(2,)]
    assert memory_store_sql("SELECT byte_offset FROM memory.external_message_cursors") == [(len(pending),)]
    assert memory_store_sql("SELECT * FROM memory.messages ORDER BY message_id")[0] == rows[0]
    assert ingest(spool).inserted == 0
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(2,)]


def test_off_does_not_open_an_explicit_missing_spool(tmp_path, monkeypatch, memory_store_sql):
    import hermes_constants

    # Given an off source and a spool that does not exist.
    monkeypatch.setattr(hermes_constants, "get_default_hermes_root", lambda: tmp_path)
    atomic_config_write(tmp_path / "config.yaml", {"memory": {"external_messages": {"enabled": False}}})

    # When explicit ingestion is requested.
    result = ingest(tmp_path / "absent.jsonl")

    # Then neither file access nor cursor creation occurs.
    assert result.inserted == result.lines == 0
    assert memory_store_sql("SELECT count(*) FROM memory.external_message_cursors") == [(0,)]
