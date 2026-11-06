"""Synthetic schema-v31 fixture using the exact legacy user's column sets."""

from __future__ import annotations

import hashlib
import json
import sqlite3
from contextlib import closing
from pathlib import Path

from hermes_state_common import SCHEMA_SQL
from hermes_state_store import TEXT_FIELDS

MESSAGE_COLUMNS = """id session_id role content tool_call_id tool_calls tool_name effect_disposition
timestamp token_count finish_reason reasoning reasoning_content reasoning_details codex_reasoning_items
codex_message_items platform_message_id observed _compressed_summary active compacted api_content
display_kind display_metadata display_identity display_order message_uid absorbed_message_uids
tool_call_uids tool_call_uid""".split()
SESSION_COLUMNS = """id source created_source user_id session_key chat_id chat_type thread_id display_name
origin_json expiry_finalized model model_config system_prompt system_prompt_hash parent_session_id started_at
ended_at end_reason message_count tool_call_count input_tokens output_tokens cache_read_tokens cache_write_tokens
reasoning_tokens cwd git_branch git_repo_root git_metadata_generation billing_provider billing_base_url billing_mode
estimated_cost_usd actual_cost_usd cost_status cost_source pricing_version title title_source last_activity_at
last_activity_description last_activity_provenance api_call_count handoff_state handoff_platform handoff_error
compression_failure_cooldown_until compression_failure_error compression_fallback_streak compression_ineffective_count
compression_recovery_deadline compression_overload_streak profile_name transport_profile rewind_count archived
auto_archived pinned hidden last_read_at tool_names""".split()


def generate(path: Path, count: int = 257000) -> dict:
    """Generate text only, repeated UIDs, pruned variants and compression/branch lineage."""
    with closing(sqlite3.connect(":memory:")) as declared, closing(sqlite3.connect(path)) as conn:
        declared.executescript(SCHEMA_SQL)
        definitions = {}
        for table, names in (("sessions", SESSION_COLUMNS), ("transcript_rows", MESSAGE_COLUMNS)):
            types = {r[1]: r[2] for r in declared.execute(f"PRAGMA table_info({table})")}
            types.update({field: "TEXT" for field in TEXT_FIELDS})
            definitions[table] = ", ".join(f'"{name}" {types[name]}' + (" PRIMARY KEY" if name == "id" else "")
                                            for name in names)
        conn.execute("CREATE TABLE schema_version(version INTEGER)")
        conn.execute("INSERT INTO schema_version VALUES (31)")
        conn.execute(f"CREATE TABLE sessions ({definitions['sessions']})")
        conn.execute(f"CREATE TABLE messages ({definitions['transcript_rows']})")
        conn.execute("CREATE INDEX messages_session ON messages(session_id)")
        for index in range(6000):
            group, child = divmod(index, 3)
            conn.execute("INSERT INTO sessions (id, source, created_source, user_id, session_key, origin_json, "
                         "parent_session_id, model_config, started_at, ended_at, end_reason, message_count) "
                         "VALUES (?, 'telegram', 'telegram', 'synthetic-owner', ?, ?, ?, ?, ?, ?, ?, 0)",
                         (f"s{index}", f"telegram:synthetic:{group}", '{"platform":"telegram"}',
                          f"s{index-child}" if child else None,
                          json.dumps({"_branched_from": f"s{index-child}"}) if child == 2 else None,
                          1700000000 + index, 1700000001 + index if not child else None,
                          "compression" if not child else None))
        names = ", ".join(MESSAGE_COLUMNS)
        sql = f"INSERT INTO messages ({names}) VALUES ({','.join('?' for _ in MESSAGE_COLUMNS)})"
        batch = []
        for i in range(count):
            number = i % 54000
            uid = f"synthetic-{number}"
            version = (i // 54000) % 2 if number % 4 == 0 else 0
            seed = hashlib.sha256(f"{number}:{version}".encode()).hexdigest()
            body = (f"Generated synthetic {number} version {version}: " + seed * 55)[:3500]
            row = dict(id=i + 1, session_id=f"s{(i // 43) % 6000}", role="assistant", content=body,
                       tool_call_id=f"call-{number}", tool_calls=json.dumps([{"id": f"call-{number}", "type": "function"}]),
                       tool_name="synthetic", effect_disposition="observed", timestamp=1700000000.25 + i,
                       token_count=950, finish_reason="stop", reasoning=seed * 2, reasoning_content=seed,
                       reasoning_details=json.dumps({"version": version}), codex_reasoning_items='[{"kind":"reason"}]',
                       codex_message_items='[{"kind":"message"}]', platform_message_id=str(number), observed=1,
                       _compressed_summary=int(i % 3 == 1), active=int(i % 3 != 0), compacted=int(i % 3 == 0),
                       api_content=json.dumps([{"type": "text", "text": seed}]), display_kind="message",
                       display_metadata='{"generated":true}', display_identity=b"synthetic", display_order=i,
                       message_uid=uid, absorbed_message_uids='["synthetic-ancestor"]',
                       tool_call_uids='["synthetic-call"]', tool_call_uid="synthetic-call")
            batch.append(tuple(row.get(name) for name in MESSAGE_COLUMNS))
            if len(batch) == 256:
                conn.executemany(sql, batch)
                batch.clear()
        conn.executemany(sql, batch)
        conn.commit()
        return {"schema_version": 31, "rows": count,
                "nonnull_uids": conn.execute("SELECT count(DISTINCT message_uid) FROM messages").fetchone()[0],
                "null_uids": conn.execute("SELECT count(*) FROM messages WHERE message_uid IS NULL").fetchone()[0],
                "sessions": 6000, "bytes": path.stat().st_size,
                "message_columns": MESSAGE_COLUMNS, "session_columns": SESSION_COLUMNS}
