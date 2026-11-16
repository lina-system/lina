"""lina#308: the store writes the text of a job marked personal with its ID and account numbers masked, through its one
write path, while the same number from an unmarked job stays as written. Runs on the real Postgres of
tests/_fixtures/memory_store.py; every number is synthetic."""

from __future__ import annotations

import json

import pytest

from hermes_state import SessionDB
from hermes_state_personal import PersonalMarks

NUMBER = "900101-1234567"
MASKED = "******-***4567"
LINE = f"주민등록번호 {NUMBER}, 계좌 110-123-456789"


@pytest.fixture
def db(tmp_path):
    db = SessionDB(db_path=tmp_path / "state.db")
    for session in ("marked", "plain", "marked-child"):
        db.create_session(session, "cli")
    yield db
    db.close()


def _stored(memory_store_sql, session_id: str) -> list[tuple]:
    return memory_store_sql("SELECT role, content, native::text FROM memory.messages WHERE session_id = %s"
                            " ORDER BY message_id", (session_id,))


def test_a_number_from_a_marked_job_is_stored_masked_and_the_same_number_unmarked_is_not(db, memory_store_sql):
    PersonalMarks(db.message_store).mark("marked", "/synthetic/docs/contract.pdf", "owner")

    db.append_message("marked", "user", content=LINE)
    db.append_message("marked", "assistant", content="확인했어요.", reasoning=f"the number is {NUMBER}",
                      tool_calls=[{"id": "c1", "type": "function",
                                   "function": {"name": "note", "arguments": json.dumps({"id": NUMBER})}}])
    db.append_message("plain", "user", content=LINE)

    marked = _stored(memory_store_sql, "marked")
    assert marked[0][1] == f"주민등록번호 {MASKED}, 계좌 ***-***-**6789"
    assert not any(NUMBER in (content or "") or NUMBER in native for _, content, native in marked)
    assert MASKED in marked[1][2]  # the reasoning and the tool call arguments, kept in native
    assert [content for _, content, _ in _stored(memory_store_sql, "plain")] == [LINE]

    transcript = db.get_messages("marked")
    assert transcript[0]["content"] == f"주민등록번호 {MASKED}, 계좌 ***-***-**6789"
    assert db.get_messages("plain")[0]["content"] == LINE


def test_a_compaction_child_session_continues_the_marked_job(db, memory_store_sql):
    marks = PersonalMarks(db.message_store)
    marks.mark("marked", "/synthetic/docs/contract.pdf", "owner")
    assert marks.continue_job("marked", "marked-child")
    assert not marks.continue_job("plain", "unrelated-child")

    db.append_message("marked-child", "user", content=LINE)

    assert [content for _, content, _ in _stored(memory_store_sql, "marked-child")] == [
        f"주민등록번호 {MASKED}, 계좌 ***-***-**6789"]
    assert marks.job_of("marked-child") == "marked"


def test_gateway_notes_of_a_marked_job_are_stored_masked(db, memory_store_sql):
    PersonalMarks(db.message_store).mark("marked", "/synthetic/docs/id-card.png", "owner")

    db.append_message("marked", "user", content="이거 봐줘", display_metadata={
        "gateway_notes": {"parts": [{"kind": "image_reading", "text": f"The card reads {NUMBER}."}]}})

    parts = memory_store_sql("SELECT content FROM memory.message_parts")
    assert parts == [(f"The card reads {MASKED}.",)]
