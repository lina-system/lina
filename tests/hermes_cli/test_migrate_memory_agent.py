"""Synthetic legacy state imports against the real PostgreSQL memory fixture."""

from __future__ import annotations

import sqlite3
from contextlib import closing

import pytest

from hermes_cli.migrate_memory_agent import migrate_agent, verify_agent
from hermes_state_common import SCHEMA_SQL
from hermes_state_store import TEXT_FIELDS, MessageStore, payload_digest, rows_with_text


def _snapshot(tmp_path, version=32):
    source = tmp_path / "snapshot.db"
    destination = tmp_path / "profiles" / "migrated" / "state.db"
    with closing(sqlite3.connect(source)) as conn:
        conn.executescript(SCHEMA_SQL)
        conn.execute("INSERT INTO schema_version VALUES (?)", (version,))
        # Derive placement columns from today's declaration; legacy text is explicit.
        columns = conn.execute("PRAGMA table_info(transcript_rows)").fetchall()
        definitions = [f'"{r[1]}" {r[2]}' for r in columns
                       if r[1] not in {"text_uid", "content_digest", "tool_call_count"}]
        conn.execute("CREATE TABLE messages (" + ", ".join(definitions) + ", " +
                     ", ".join(f"{field} TEXT" for field in TEXT_FIELDS) + ")")
        for sid, parent, config in [("root", None, None), ("compressed", "root", None),
                                    ("branch", "root", '{"_branched_from":"root"}')]:
            conn.execute("INSERT INTO sessions (id, source, started_at, parent_session_id, model_config, "
                         "session_key, user_id, origin_json) VALUES (?, 'telegram', 1700000000, ?, ?, "
                         "'telegram:chat:12', '42', '{\"platform\":\"telegram\"}')", (sid, parent, config))
        conn.execute("UPDATE sessions SET end_reason='compression', ended_at=1700000001 WHERE id='root'")
        conn.execute("CREATE TABLE operational_receipt (receipt TEXT)")
        conn.execute("INSERT INTO operational_receipt VALUES ('retain-me')")
        for row_id, sid, uid, content in [(1, "root", "shared", "ORIGINAL-SECRET-712"),
                                          (2, "compressed", "shared", "ORIGINAL-SECRET-712"),
                                          (3, "branch", "shared", "pruned"), (4, "branch", None, "no uid")]:
            row = dict(id=row_id, session_id=sid, role="assistant", timestamp=1700000000.25 + row_id,
                       message_uid=uid, content=content, tool_calls='[ {"id": "call", "x": 1} ]',
                       reasoning="raw reasoning", reasoning_content="reasoning content", reasoning_details='{"a":1}',
                       codex_reasoning_items='[{"r":2}]', codex_message_items='[{"m":3}]',
                       api_content='[{"type":"text","text":"api"}]', active=int(row_id != 1),
                       compacted=int(row_id == 1), _compressed_summary=int(row_id == 2), observed=1,
                       display_identity=b"binary\x00identity", display_order=17, display_metadata='{"x":2}',
                       token_count=83, tool_call_uids='["tool-uid"]', absorbed_message_uids='["absorbed"]')
            conn.execute(f"INSERT INTO messages ({', '.join(row)}) VALUES ({', '.join('?' for _ in row)})",
                         tuple(row.values()))
        conn.commit()
    return source, destination


@pytest.mark.parametrize("version", [32, 33])
def test_import_preserves_versions_placements_and_operational_copy(tmp_path, memory_store_sql, version):
    # Given a compression copy, a pruned branch version, and a missing legacy UID.
    source, destination = _snapshot(tmp_path, version)
    before = source.read_bytes()
    # When the copied agent is imported.
    report = migrate_agent(source, destination, "migrated")
    # Then placements are counted, not distinct shared texts, and all payloads survive.
    assert report.ok and (report.inserted, report.matched) == (4, 0)
    assert source.read_bytes() == before
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(3,)]
    with closing(sqlite3.connect(destination)) as conn, closing(MessageStore("migrated")) as store:
        conn.row_factory = sqlite3.Row
        placements = conn.execute("SELECT * FROM transcript_rows ORDER BY id").fetchall()
        joined = rows_with_text(store, placements)
        assert placements[0]["text_uid"] == placements[1]["text_uid"]
        assert placements[2]["text_uid"] != placements[0]["text_uid"]
        assert [r["content"] for r in joined] == ["ORIGINAL-SECRET-712", "ORIGINAL-SECRET-712", "pruned", "no uid"]
        assert joined[0]["tool_calls"] == '[ {"id": "call", "x": 1} ]'
        assert joined[0]["display_identity"] == b"binary\x00identity"
        assert (joined[0]["active"], joined[0]["compacted"], joined[1]["_compressed_summary"]) == (0, 1, 1)
        assert conn.execute("SELECT receipt FROM operational_receipt").fetchone()[0] == "retain-me"
        assert conn.execute("SELECT 1 FROM sqlite_master WHERE name='messages'").fetchone() is None
        assert report.source[0].sha256 == payload_digest(tuple(joined[0].get(f) for f in TEXT_FIELDS))
    assert b"ORIGINAL-SECRET-712" not in destination.read_bytes()
    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE conversation_id IS NULL") == [(0,)]
    keys = memory_store_sql("SELECT kind, source, key FROM memory.identity_keys")
    assert ("agent", "profile", "migrated") in keys
    assert ("person", "gateway_user", "telegram:42") in keys
    assert ("conversation", "gateway_chat", "migrated:telegram:chat:12") in keys


def test_rerun_matches_all_placements_without_adding_text(tmp_path, memory_store_sql):
    # Given an already migrated source.
    source, destination = _snapshot(tmp_path)
    migrate_agent(source, destination, "migrated")
    # When the adapter runs again.
    report = migrate_agent(source, destination, "migrated")
    # Then it verifies the actual join and counts every placement as matched.
    assert report.ok and (report.inserted, report.matched) == (0, 4)
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(3,)]


@pytest.mark.parametrize("damage", ["placement", "text", "extra", "extra_missing_text", "api"])
@pytest.mark.parametrize("report_limit", [4096, 2])
def test_verification_detects_missing_extra_and_changed_destination(
        tmp_path, memory_store_sql, damage, report_limit, monkeypatch):
    # Given a completed migration damaged after its cache has been filled.
    monkeypatch.setattr("hermes_cli.migrate_memory_agent_stream.SMALL_REPORT_ROWS", report_limit)
    source, destination = _snapshot(tmp_path)
    migrate_agent(source, destination, "migrated")
    with closing(sqlite3.connect(destination)) as conn:
        if damage == "placement":
            conn.execute("DELETE FROM transcript_rows WHERE id=4")
        if damage == "extra":
            conn.execute("INSERT INTO transcript_rows (session_id, role, timestamp, text_uid) "
                         "SELECT session_id, role, timestamp, text_uid FROM transcript_rows WHERE id=4")
        if damage == "extra_missing_text":
            conn.execute("INSERT INTO transcript_rows (session_id, role, timestamp, text_uid) "
                         "VALUES ('branch', 'user', 1, 'missing-text')")
        conn.commit()
    if damage == "text":
        memory_store_sql("DELETE FROM memory.messages WHERE uid LIKE %s", ("%legacy:%",))
    if damage == "api":
        memory_store_sql("UPDATE memory.messages SET native=jsonb_set(native, '{api_content}', "
                         "'[\"s\",\"changed\"]'::jsonb)")
    # When verification/rerun inspects PostgreSQL instead of trusting cache or version.
    if damage == "api":
        with pytest.raises(ValueError, match="damaged"):
            verify_agent(source, destination, "migrated")
    else:
        report = verify_agent(source, destination, "migrated")
        assert not report.ok
    # Then a rerun cannot silently call the corrupted destination successful.
    with pytest.raises(ValueError):
        migrate_agent(source, destination, "migrated")


def test_unrelated_destination_is_not_overwritten(tmp_path):
    # Given a separate legacy database with colliding row IDs but different text.
    source, destination = _snapshot(tmp_path)
    destination.parent.mkdir(parents=True)
    with closing(sqlite3.connect(source)) as src, closing(sqlite3.connect(destination)) as dst:
        src.backup(dst)
        dst.execute("UPDATE messages SET content='unrelated' WHERE id=1")
        dst.commit()
    before = destination.read_bytes()
    # When migration sees a destination that is not the copied source.
    with pytest.raises(ValueError, match="not the source copy"):
        migrate_agent(source, destination, "migrated")
    # Then it fails without replacing that database.
    assert destination.read_bytes() == before


@pytest.mark.parametrize("invalid", ["new", "malformed", "mixed"])
def test_rejects_unsupported_or_mixed_source_before_creating_destination(tmp_path, invalid):
    # Given a source outside the adapter's legacy capability.
    source, destination = _snapshot(tmp_path)
    with closing(sqlite3.connect(source)) as conn:
        if invalid == "new":
            conn.execute("UPDATE schema_version SET version=34")
        if invalid == "malformed":
            conn.execute("DROP TABLE messages")
        if invalid == "mixed":
            conn.execute("INSERT INTO transcript_rows (session_id, role, timestamp) VALUES ('root','user',1)")
        conn.commit()
    # When migration parses the source.
    with pytest.raises(ValueError):
        migrate_agent(source, destination, "migrated")
    # Then no destination is created.
    assert not destination.exists()


def test_registry_fallback_is_not_counted_as_success(tmp_path, monkeypatch, memory_store_sql):
    from plugins.memory.lina.identity import IdentityRegistry, derived_id

    # Given registry resolution falling back without persisting any keys.
    source, destination = _snapshot(tmp_path)
    monkeypatch.setattr(IdentityRegistry, "resolve", lambda self, kind, keys: derived_id(kind, *keys[0]))
    # When migration checks the registration receipt.
    with pytest.raises(ValueError, match="did not persist"):
        migrate_agent(source, destination, "migrated")
    # Then text has not moved and legacy messages remain recoverable.
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(0,)]
    with closing(sqlite3.connect(destination)) as conn:
        assert conn.execute("SELECT count(*) FROM messages").fetchone() == (4,)


def test_unrelated_shared_uid_is_not_reused(tmp_path, memory_store_sql):
    # Given an existing shared UID owned by unrelated text for the target agent.
    source, destination = _snapshot(tmp_path)
    with closing(MessageStore("migrated")) as store:
        with store.write():
            from hermes_state_store import put_row_text
            put_row_text(store, {"message_uid": "shared", "content": "unrelated", "role": "assistant",
                                 "timestamp": 1, "session_id": "other"}, row_id=1)
    # When the legacy import encounters that UID.
    with pytest.raises(ValueError, match="unrelated destination text UID"):
        migrate_agent(source, destination, "migrated")
    # Then it preserves the unrelated row rather than treating it as a source variant.
    assert memory_store_sql("SELECT content FROM memory.messages") == [("unrelated",)]


def test_legacy_lcm_conversation_alias_resolves_without_migrating_nodes(tmp_path, memory_store_sql):
    source, destination = _snapshot(tmp_path)
    with closing(sqlite3.connect(tmp_path / "lcm.db")) as conn:
        conn.execute("CREATE TABLE messages(session_id TEXT, conversation_id TEXT)")
        conn.execute("INSERT INTO messages VALUES ('root','legacy-conversation')")
        conn.commit()

    migrate_agent(source, destination, "migrated")

    ids = memory_store_sql("SELECT identity_id FROM memory.identity_keys WHERE kind='conversation' "
                           "AND (source,key) IN (('gateway_chat','migrated:telegram:chat:12'), "
                           "('lcm_conversation','migrated:legacy-conversation'), "
                           "('hermes_session','migrated:root'))")
    assert len(ids) == 3 and len(set(ids)) == 1


def test_migration_bounds_live_writer_and_verification_batches(tmp_path, memory_store_sql, monkeypatch):
    # Given more unique payloads than fit in a migration batch, including later UID variants.
    source, destination = _snapshot(tmp_path)
    with closing(sqlite3.connect(source)) as conn:
        conn.executemany("INSERT INTO messages (id, session_id, role, timestamp, message_uid, content) "
                         "VALUES (?, 'branch', 'user', 1, ?, ?)",
                         ((i, f"scale-{i}", f"payload-{i}" * 100) for i in range(5, 605)))
        conn.execute("UPDATE messages SET _compressed_summary=0, active=1, compacted=0 WHERE id>=5")
        conn.commit()
    original_put = MessageStore.put_text
    original_export = MessageStore.export_texts
    staged_sizes, export_sizes = [], []

    def bounded_put(self, **kwargs):
        result = original_put(self, **kwargs)
        staged_sizes.append(len(self._staged))
        assert len(self._staged) <= 256, "migration retained more than one writer batch"
        return result

    def bounded_export(self, uids):
        batch = list(uids)
        export_sizes.append(len(batch))
        assert len(batch) <= 512, "migration exported an unbounded UID collection"
        yield from original_export(self, batch)

    monkeypatch.setattr(MessageStore, "put_text", bounded_put)
    monkeypatch.setattr(MessageStore, "export_texts", bounded_export)
    # When the real writer imports and verifies all placements, then verifies a rerun.
    report = migrate_agent(source, destination, "migrated")
    repeated = migrate_agent(source, destination, "migrated")
    # Then transactions and exports remain bounded without losing payloads or duplicating rows.
    assert report.ok and report.inserted == 604
    assert repeated.ok and repeated.matched == 604
    assert max(staged_sizes) <= 256 and max(export_sizes) <= 512
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(603,)]
