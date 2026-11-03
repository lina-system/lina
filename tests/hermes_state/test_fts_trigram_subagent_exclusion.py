"""Delegate transcripts remain searchable with word and scoped CJK queries."""

import pytest

from hermes_state import SessionDB


@pytest.fixture
def db(tmp_path):
    with SessionDB(db_path=tmp_path / "state.db") as session_db:
        yield session_db


def _seed(db: SessionDB) -> dict[str, int]:
    db.create_session("root", source="cli")
    # delegate_tool children: source='subagent' via platform, plus the
    # _delegate_from creation marker.
    db.create_session(
        "kid", source="subagent", parent_session_id="root",
        model_config={"_delegate_from": "root"},
    )
    # A child spawned under a gateway turn inherits the gateway's source but
    # still carries the marker.
    db.create_session(
        "gw-kid", source="telegram", parent_session_id="root",
        model_config={"_delegate_from": "root"},
    )
    # Compression continuation: parent_session_id but NO marker -> indexed.
    db.create_session("cont", source="cli", parent_session_id="root")
    return {
        "root": db.append_message("root", role="user", content="交付状态正常 root-word"),
        "kid": db.append_message("kid", role="assistant", content="子任务状态正常 kid-word"),
        "gw-kid": db.append_message("gw-kid", role="assistant", content="网关子任务 gwkid-word"),
        "cont": db.append_message("cont", role="assistant", content="继续会话内容 cont-word"),
    }


def test_subagent_rows_remain_word_searchable(db: SessionDB):
    _seed(db)
    assert [r["session_id"] for r in db.search_messages("kid-word")] == ["kid"]
    assert [r["session_id"] for r in db.search_messages("gwkid-word")] == ["gw-kid"]
    # Explicit CJK search scoped to the excluded source falls back to LIKE.
    assert [
        r["session_id"]
        for r in db.search_messages("子任务状态", source_filter=["subagent"])
    ] == ["kid"]
    # Top-level CJK substring search unaffected.
    assert [r["session_id"] for r in db.search_messages("交付状态")] == ["root"]
