"""Search includes long tool payloads only when explicitly requested."""

import pytest

from hermes_state import SessionDB


def _long_message(prefix: str, tail: str) -> str:
    return f"{prefix} {'padding ' * 1200} {tail}"


@pytest.fixture
def db(tmp_path):
    with SessionDB(db_path=tmp_path / "state.db") as session_db:
        session_db.create_session("session", source="cli")
        yield session_db


def test_explicit_tool_search_reads_complete_payload(db):
    tool_id = db.append_message(
        "session",
        role="tool",
        content=_long_message("indexed-prefix-token", "tool-tail-token"),
        tool_name="terminal",
    )
    user_id = db.append_message(
        "session",
        role="user",
        content=_long_message("user-prefix-token", "user-tail-token"),
    )

    assert [row["id"] for row in db.search_messages("indexed-prefix-token")] == [
        tool_id
    ]
    assert db.search_messages("tool-tail-token", role_filter=["user", "assistant"]) == []
    assert [
        row["id"]
        for row in db.search_messages("tool-tail-token", role_filter=["tool"])
    ] == [tool_id]
    assert [row["id"] for row in db.search_messages("user-tail-token")] == [
        user_id
    ]


