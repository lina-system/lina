"""lina#12: restored text is the same as stored text only when its whole payload is, not just its content."""

import json
from pathlib import Path

import pytest

from hermes_cli import profiles
from hermes_cli.profiles import create_profile
from hermes_state import SessionDB
from hermes_state_store import (
    TEXT_CACHE, TEXT_FIELDS, MessageTextRestoreError, _native, _values_from_row, payload_digest,
)

TOOL_CALLS = [{"id": "c1", "type": "function", "function": {"name": "read", "arguments": "{\"p\": 1}"}}]
OTHER_TOOL_CALLS = json.dumps([{"id": "c1", "type": "function", "function": {"name": "write", "arguments": "{}"}}])


@pytest.fixture()
def profile_env(tmp_path, monkeypatch):
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    home = tmp_path / ".hermes"
    home.mkdir(exist_ok=True)
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def _text_rows(profile: Path, sid: str) -> list[dict]:
    TEXT_CACHE.clear()
    db = SessionDB(profile / "state.db")
    try:
        return [{field: row.get(field) for field in TEXT_FIELDS} for row in db.get_messages(sid)]
    finally:
        db.close()


def _export_with_destination_row(tmp_path, memory_store_sql, field, archived, destination_value):
    """Export profile ``original`` holding one message, and give agent ``copy`` that message's uid with
    *destination_value* in *field* (the archived value keeps the payload identical)."""
    source = create_profile("original", no_alias=True)
    db = SessionDB(source / "state.db")
    try:
        db.create_session("s", "cli")
        db.append_message("s", "assistant", content="same words", **{field: archived})
        (text_uid,) = [r[0] for r in db._conn.execute("SELECT text_uid FROM transcript_rows")]
    finally:
        db.close()
    expected = _text_rows(source, "s")
    archive = profiles.export_profile("original", str(tmp_path / "p.tar.gz"))
    content, native = memory_store_sql("SELECT content, native FROM memory.messages WHERE uid = %s", (text_uid,))[0]
    values = list(_values_from_row(content, native))
    values[TEXT_FIELDS.index(field)] = destination_value
    clash = f"agent:copy:{text_uid.split(':', 2)[2]}"
    memory_store_sql(
        "INSERT INTO memory.messages (uid, source, agent_id, content, native, content_sha256)"
        " SELECT %s, 'agent', 'copy', content, %s::jsonb, content_sha256 FROM memory.messages WHERE uid = %s",
        (clash, json.dumps(_native(values, {}, payload_digest(values))), text_uid))
    return archive, expected, clash


@pytest.mark.parametrize("field, archived, other", [
    ("tool_calls", TOOL_CALLS, OTHER_TOOL_CALLS),
    ("api_content", "archived api payload", "the destination's api payload"),
])
def test_an_archived_row_whose_uid_holds_another_payload_is_refused(
        profile_env, tmp_path, memory_store_sql, field, archived, other):
    archive, _, clash = _export_with_destination_row(tmp_path, memory_store_sql, field, archived, other)
    before = memory_store_sql("SELECT uid, native FROM memory.messages ORDER BY uid")

    with pytest.raises(MessageTextRestoreError, match=clash):
        profiles.import_profile(str(archive), name="copy")

    assert not (profile_env / "profiles" / "copy").exists()
    assert memory_store_sql("SELECT uid, native FROM memory.messages ORDER BY uid") == before


def test_an_archived_row_whose_uid_holds_the_same_payload_is_accepted(profile_env, tmp_path, memory_store_sql):
    archive, expected, _ = _export_with_destination_row(
        tmp_path, memory_store_sql, "tool_calls", TOOL_CALLS, json.dumps(TOOL_CALLS))

    restored = profiles.import_profile(str(archive), name="copy")

    assert _text_rows(restored, "s") == expected
