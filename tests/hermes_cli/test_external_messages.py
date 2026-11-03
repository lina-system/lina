"""Synthetic collector spool -> real Postgres contracts (lina#18)."""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys

import psycopg
import pytest

from hermes_cli.external_messages import SpoolError, ingest
from hermes_state_store import MessageStore


def message(source="discord", room="room-a", native="1", **changes):
    identity = json.dumps([source, room, native], separators=(",", ":"), ensure_ascii=False)
    return {"source": source, "room_id": room, "native_id": native,
            "uid": hashlib.sha256(identity.encode()).hexdigest(), "account": "synthetic-account",
            "room_name": "Synthetic room", "author_id": "writer", "author_name": "Writer",
            "ts": "2026-01-02T03:04:05Z", "edited_ts": None, "deleted": 0,
            "reply_to_uid": None, "thread_root_uid": None, "text": "synthetic hello",
            "attachments": [], "raw": {"private": "must never persist"}, "ingest_path": "live",
            "privacy_class": "private", "filled_from": None, **changes}


def event(*messages, number=1):
    return (json.dumps({"version": 1, "event_id": f"{number:032x}",
                        "ingested_at": "2026-01-02T03:04:06.000000Z", "messages": list(messages),
                        "observations": [], "sync_checkpoint": {"ignored": "collector cursor"}},
                       ensure_ascii=False) + "\n").encode()


def test_replay_versions_precedence_and_completed_tail(tmp_path, memory_store_sql):
    spool = tmp_path / "events.jsonl"
    original = message()
    archive = message(text="archive text", ingest_path="archive")
    edited = message(text="edited archive", ingest_path="archive", edited_ts="2026-01-03T00:00:00Z")
    deleted = {**edited, "deleted": 1}
    tail = event(message("line", "room-c"), number=9)
    data = b"".join([event(original), event(original, number=2),
                     event(message("kakao", "room-b"), message("imessage", "room-d"), number=3),
                     event(archive, number=4), event(message(text="late live"), number=5),
                     event(edited, number=6), event(deleted, number=7)])
    spool.write_bytes(data + tail[:-1])

    first = ingest(spool)
    before = memory_store_sql("SELECT uid, content, native FROM memory.messages ORDER BY message_id")
    second = ingest(spool)
    assert (first.lines, first.inserted, second.lines, second.inserted) == (7, 7, 0, 0)
    assert memory_store_sql("SELECT uid, content, native FROM memory.messages ORDER BY message_id") == before
    head = memory_store_sql("SELECT m.content, m.native->>'deleted' FROM memory.external_message_heads h "
                            "JOIN memory.messages m ON m.uid=h.current_uid WHERE h.uid=%s",
                            ("messages:" + original["uid"],))
    assert head == [("edited archive", "1")]
    assert all("raw" not in native and "sync_checkpoint" not in native for _, _, native in before)
    assert before[0][0] == "messages:" + original["uid"]
    assert all(len(uid.rsplit("#", 1)[1]) == 16 for uid, _, _ in before if "#" in uid)

    with spool.open("ab") as stream:
        stream.write(b"\n" + event(message("discord", "new-room"), number=10))
    appended = ingest(spool)
    assert (appended.lines, appended.inserted, appended.offset) == (2, 2, spool.stat().st_size)
    assert ingest(spool).inserted == 0


@pytest.mark.parametrize("replacement", [b"", b"X"])
def test_changed_consumed_prefix_stops_without_writes(tmp_path, memory_store_sql, replacement):
    spool = tmp_path / "events.jsonl"
    original = event(message())
    spool.write_bytes(original)
    ingest(spool)
    cursor = memory_store_sql("SELECT * FROM memory.external_message_cursors")
    spool.write_bytes(replacement + original[1:] if replacement else b"")
    with pytest.raises(SpoolError, match="truncated|rewritten"):
        ingest(spool)
    assert memory_store_sql("SELECT * FROM memory.external_message_cursors") == cursor
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(1,)]


def test_failure_rolls_back_rows_and_cursor_then_replays(tmp_path, memory_store_sql):
    spool = tmp_path / "events.jsonl"
    spool.write_bytes(event(message(), message(native="2", text="fail this batch")))
    memory_store_sql("ALTER TABLE memory.messages ADD CONSTRAINT synthetic_reject CHECK (content <> 'fail this batch')")
    try:
        with pytest.raises(psycopg.errors.CheckViolation):
            ingest(spool)
        assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(0,)]
        assert memory_store_sql("SELECT count(*) FROM memory.external_message_cursors") == [(0,)]
    finally:
        memory_store_sql("ALTER TABLE memory.messages DROP CONSTRAINT synthetic_reject")
    assert ingest(spool).inserted == 2


def test_forced_masking_covers_content_and_metadata(tmp_path, memory_store_sql, monkeypatch):
    from agent import redact
    monkeypatch.setattr(redact, "_redact_enabled", lambda: False)
    fake = "ghp_" + "SyntheticCredential1234567890abcdefABCD"
    spool = tmp_path / "events.jsonl"
    spool.write_bytes(event(message(text=f"credential {fake}", author_name=fake,
                                   attachments=[{"caption": fake, "url": f"https://example.test/?token={fake}"}])))
    ingest(spool)
    rows = memory_store_sql("SELECT content, native, content_sha256 FROM memory.messages")
    assert fake not in json.dumps(rows)
    content, native, digest = rows[0]
    assert digest == hashlib.sha256(content.encode()).hexdigest()
    assert "raw" not in native


def test_shared_rows_readable_but_not_writable_and_private_rows_isolated(tmp_path, memory_store_sql, _memory_store):
    spool = tmp_path / "events.jsonl"
    spool.write_bytes(event(message()))
    ingest(spool)
    memory_store_sql("INSERT INTO memory.messages(uid,source,agent_id,scope,content,content_sha256) "
                     "VALUES ('private-a','agent','a','agent','private','digest')")
    for agent in ("a", "b"):
        with psycopg.connect(_memory_store.app_dsn(), autocommit=True) as conn:
            conn.execute("SELECT set_config('lina.agent_id', %s, false)", (agent,))
            rows = conn.execute("SELECT uid, author, conversation_id, scope, forgotten_at FROM memory.messages "
                                "WHERE source='messages'").fetchall()
            assert rows == [("messages:" + message()["uid"], "discord:writer", "messages:discord:room-a", "agents", None)]
            assert conn.execute("SELECT uid FROM memory.messages WHERE uid='private-a'").fetchall() == (
                [("private-a",)] if agent == "a" else [])
            assert conn.execute("UPDATE memory.messages SET content='tampered' WHERE source='messages'").rowcount == 0
            assert conn.execute("DELETE FROM memory.messages WHERE source='messages'").rowcount == 0
            for owner in (None, "another-agent"):
                with pytest.raises(psycopg.errors.InsufficientPrivilege):
                    conn.execute("INSERT INTO memory.messages(uid,source,agent_id,scope,content_sha256) "
                                 "VALUES ('forged','messages',%s,'agents','digest')", (owner,))
            with pytest.raises(psycopg.errors.InsufficientPrivilege):
                conn.execute("SELECT * FROM memory.external_message_cursors")
    assert memory_store_sql("SELECT content FROM memory.messages WHERE source='messages'") == [("synthetic hello",)]


def test_gc_does_not_even_consider_external_rows(tmp_path, memory_store_sql):
    spool = tmp_path / "events.jsonl"
    spool.write_bytes(event(message()))
    ingest(spool)
    memory_store_sql("UPDATE memory.messages SET observed_at=now()-interval '60 days'")
    considered = []
    store = MessageStore("default")
    try:
        assert store.collect_unreferenced(lambda uids: considered.extend(uids) or set()) == 0
    finally:
        store.close()
    assert considered == []
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(1,)]


def test_cli_parser_no_config_and_explicit_spool(tmp_path, capsys):
    from hermes_cli.subcommands.memory import build_memory_parser
    parser = argparse.ArgumentParser()
    build_memory_parser(parser.add_subparsers(), cmd_memory=lambda args: pytest.fail("wrong command"))
    args = parser.parse_args(["memory", "external-messages", "ingest"])
    args.func(args)
    assert "no spool configured" in capsys.readouterr().out
    spool = tmp_path / "events.jsonl"
    spool.write_bytes(event(message()))
    args = parser.parse_args(["memory", "external-messages", "ingest", "--spool", str(spool)])
    args.func(args)
    assert "1 rows inserted" in capsys.readouterr().out


def test_real_cli_configured_spool_uses_owner_store(tmp_path, monkeypatch, _memory_store, memory_store_sql):
    from hermes_cli.config import atomic_config_write
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("LINA_HOME", str(home))
    spool = tmp_path / "events.jsonl"
    spool.write_bytes(event(message("line")))
    atomic_config_write(home / "config.yaml", {"memory": {"external_messages": {"spool": str(spool)}}})
    # The real CLI resolves the installed owner DSN, which always names database lina.
    # Use a subprocess entrypoint with only the fixture's database resolver substituted.
    script = ("import sys; import hermes_state_store as s; dsn=sys.argv.pop(1); "
              "s.resolve_owner_dsn=lambda:dsn; "
              "from hermes_cli.main import main; main()")
    done = subprocess.run([sys.executable, "-c", script, _memory_store.superuser_dsn(),
                           "memory", "external-messages", "ingest"], capture_output=True, text=True,
                          encoding="utf-8", errors="replace", timeout=60, check=False)
    assert done.returncode == 0, done.stdout + done.stderr
    assert "1 rows inserted" in done.stdout
    assert memory_store_sql("SELECT author FROM memory.messages") == [("line:writer",)]
    from hermes_state import SessionDB
    db = SessionDB(home / "state.db")
    db.close()
    memory_store_sql("UPDATE memory.messages SET observed_at=now()-interval '60 days'")
    collected = subprocess.run([sys.executable, "-c", "from hermes_cli.main import main; main()",
                                "store", "gc"], capture_output=True, text=True, encoding="utf-8",
                               errors="replace", timeout=60, check=False)
    assert collected.returncode == 0, collected.stdout + collected.stderr
    assert "reclaimed 0" in collected.stdout
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(1,)]


def test_ticker_ingests_real_spool_from_root_config(tmp_path, monkeypatch, memory_store_sql):
    from cron.external_messages import maybe_ingest
    from hermes_cli.config import atomic_config_write
    import hermes_constants
    spool = tmp_path / "events.jsonl"
    spool.write_bytes(event(message("kakao")))
    monkeypatch.setattr(hermes_constants, "get_default_hermes_root", lambda: tmp_path)
    atomic_config_write(tmp_path / "config.yaml", {"memory": {"external_messages": {"spool": str(spool)}}})
    maybe_ingest()
    maybe_ingest()
    assert memory_store_sql("SELECT author FROM memory.messages") == [("kakao:writer",)]


def test_batches_resume_after_malformed_event(tmp_path, monkeypatch, memory_store_sql):
    from hermes_cli import external_messages
    monkeypatch.setattr(external_messages, "_BATCH_LINES", 1)
    spool = tmp_path / "events.jsonl"
    good = event(message())
    spool.write_bytes(good + b"{malformed}\n")
    with pytest.raises(SpoolError, match="invalid spool event at byte"):
        ingest(spool)
    assert memory_store_sql("SELECT byte_offset FROM memory.external_message_cursors") == [(len(good),)]
    # Only the uncommitted suffix may be repaired.
    spool.write_bytes(good + event(message(native="2"), number=2))
    assert ingest(spool).inserted == 1
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(2,)]


def test_archive_head_survives_later_live_and_old_version_can_return(tmp_path, memory_store_sql):
    spool = tmp_path / "events.jsonl"
    archive = message(text="archive", ingest_path="archive")
    spool.write_bytes(event(archive) + event(message(text="live later"), number=2))
    ingest(spool)
    query = ("SELECT m.content FROM memory.external_message_heads h "
             "JOIN memory.messages m ON m.uid=h.current_uid")
    assert memory_store_sql(query) == [("archive",)]
    with spool.open("ab") as stream:
        stream.write(event(message(text="export", ingest_path="export"), number=3))
        stream.write(event(archive, number=4))
    assert ingest(spool).inserted == 1
    assert memory_store_sql(query) == [("archive",)]


def test_migration_can_be_applied_twice(memory_store_sql):
    from hermes_cli.store_migrations import MIGRATIONS_DIR
    migration = (MIGRATIONS_DIR / "0005_external_messages.sql").read_text()
    memory_store_sql(migration)
    memory_store_sql(migration)
    assert memory_store_sql("SELECT count(*) FROM pg_policy WHERE polrelid='memory.messages'::regclass "
                            "AND polname='lina_shared_messages' AND polcmd='r'") == [(1,)]
