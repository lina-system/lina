"""Prove host text correspondence before handing copied placements to LCM-X."""

import hashlib
import json
from contextlib import closing

from hermes_cli.migrate_memory_agent import _readonly, _rows, _register


def index_host(state_path, ledger) -> None:
    ledger.execute("CREATE TABLE host_text(session TEXT,role TEXT,stamp REAL,digest TEXT,uid TEXT)")
    ledger.execute("CREATE INDEX host_match ON host_text(session,role,stamp,digest)")
    with closing(_readonly(state_path)) as source:
        for row in _rows(source.execute("SELECT * FROM messages ORDER BY id")):
            if isinstance(row.get("content"), str):
                ledger.execute("INSERT INTO host_text VALUES (?,?,?,?,?)",
                               (row["session_id"], row["role"], row["timestamp"],
                                hashlib.sha256(row["content"].encode()).hexdigest(), row["message_uid"]))


def message_uid(row, text, ledger, source_digest: str) -> tuple[str, bool]:
    from hermes_state_store import TEXT_FIELDS

    digest = hashlib.sha256((row.get("content") or "").encode()).hexdigest()
    stamp = row.get("observed_at") if row.get("observed_at") is not None else row["timestamp"]
    for (uid,) in ledger.execute("SELECT uid FROM host_text WHERE session=? AND role=? AND stamp=? AND digest=?",
                                (row["session_id"], row["role"], stamp, digest)):
        candidates = text.text_uids_with_digest(uid, digest)
        for candidate in text.texts(candidates).values():
            calls = candidate.values[1]
            copied_calls = row.get("tool_calls")
            if candidate.values[0] == row.get("content") and (
                    calls == copied_calls or json.loads(calls or "null") == json.loads(copied_calls or "null")):
                return uid, True
    uid = f"lcm-import:{source_digest}:{row['store_id']}"
    # The same host writer supplies historical time; the routed placement then reuses it.
    with text.write():
        text.put_text(message_uid=uid, values=(row.get("content"), row.get("tool_calls")) + (None,) * (len(TEXT_FIELDS) - 2),
                      role=row["role"], tool_name=row.get("tool_name"), tool_call_id=row.get("tool_call_id"),
                      timestamp=stamp, session_id=row["session_id"], seq_in_session=None, meta={"message_uid": uid})
    return uid, False


def conversations(source, registry, agent: str, left_out: set = frozenset()) -> dict:
    columns = {r[1] for r in source.execute("PRAGMA table_info(messages)")}
    expr = "conversation_id" if "conversation_id" in columns else "''"
    result = {}
    pairs = dict.fromkeys((session, conversation) for store_id, session, conversation in source.execute(
        f"SELECT store_id,session_id,{expr} FROM messages ORDER BY store_id") if store_id not in left_out)
    for session, conversation in pairs:
        keys = [("lcm_conversation", f"{agent}:{conversation}")] if conversation else []
        keys.append(("hermes_session", f"{agent}:{session}"))
        result[conversation or session] = _register(registry, "conversation", keys)
    if source.execute("SELECT 1 FROM sqlite_master WHERE name='lcm_lifecycle_state'").fetchone():
        for (conversation,) in source.execute("SELECT conversation_id FROM lcm_lifecycle_state WHERE conversation_id<>''"):
            if conversation not in result:
                result[conversation] = _register(registry, "conversation", [("lcm_conversation", f"{agent}:{conversation}")])
    return result
