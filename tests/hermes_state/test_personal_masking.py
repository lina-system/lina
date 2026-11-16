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


ACCOUNT = "123456789012"
LABELLED = f"계좌번호 {ACCOUNT}"
LABELLED_MASKED = "계좌번호 ********9012"


def _structured_turn(db, session_id: str) -> None:
    """One assistant row and one tool row whose numbers sit only inside JSON-backed fields: content parts, tool call
    arguments, a tool result's parts and reasoning details, each with a Korean-labelled and a plain number."""
    db.append_message(session_id, "user", content=[{"type": "text", "text": LABELLED},
                                                   {"type": "text", "text": {"value": f"번호 {NUMBER}"}}])
    db.append_message(session_id, "assistant", content=[{"type": "output_text", "text": "확인할게요."}],
                      reasoning_details=[{"type": "summary", "summary": LABELLED}],
                      tool_calls=[{"id": "c2", "type": "function",
                                   "function": {"name": "note", "arguments": json.dumps({"memo": LABELLED})}}])
    db.append_message(session_id, "tool", tool_call_id="c2",
                      content=[{"type": "text", "text": f"{LABELLED}, {NUMBER}"}])


def test_structured_fields_of_a_marked_job_are_masked_by_their_decoded_text(db, memory_store_sql):
    PersonalMarks(db.message_store).mark("marked", "/synthetic/docs/bankbook.pdf", "owner")

    _structured_turn(db, "marked")

    for _, content, native in _stored(memory_store_sql, "marked"):
        assert ACCOUNT not in (content or "") and ACCOUNT not in native
        assert NUMBER not in (content or "") and NUMBER not in native
    user, assistant, tool = db.get_messages("marked")
    assert user["content"] == [{"type": "text", "text": LABELLED_MASKED},
                               {"type": "text", "text": {"value": f"번호 {MASKED}"}}]
    assert json.loads(assistant["reasoning_details"]) == [{"type": "summary", "summary": LABELLED_MASKED}]
    [call] = assistant["tool_calls"]
    assert json.loads(call["function"]["arguments"]) == {"memo": LABELLED_MASKED}
    assert tool["content"] == [{"type": "text", "text": f"{LABELLED_MASKED}, {MASKED}"}]


SIDECAR = f"{LABELLED}\n\n[recall] a synthetic memory note"


def _api_content_backfills():
    """Each writer that stamps the ``api_content`` sidecar onto a user row already persisted, found by the content the
    turn holds: (name, call returning the rows changed)."""
    from hermes_state_worker_lifecycle import worker_api_content, worker_message_api_content
    return [
        ("set_message_api_content", lambda db, sid, row_id: db.set_message_api_content(sid, row_id, LABELLED, SIDECAR)),
        ("set_latest_user_api_content", lambda db, sid, row_id: db.set_latest_user_api_content(sid, LABELLED, SIDECAR)),
        ("worker session.message_api_content", lambda db, sid, row_id: db._execute_write(
            lambda conn: worker_message_api_content(
                db, conn, sid, {"row_id": row_id, "content": LABELLED, "api_content": SIDECAR}))["value"]),
        ("worker session.api_content", lambda db, sid, row_id: db._execute_write(
            lambda conn: worker_api_content(db, conn, sid, {"content": LABELLED, "api_content": SIDECAR}))["value"]),
    ]


@pytest.mark.parametrize("session", ["marked", "plain"])
@pytest.mark.parametrize("backfill", [call for _, call in _api_content_backfills()],
                         ids=[name for name, _ in _api_content_backfills()])
def test_a_sidecar_backfill_finds_a_user_row_persisted_before_it(db, tmp_path, session, backfill):
    """The row is written first (preflight compaction, an early flush), so the backfill finds it by the content the turn
    holds: for a marked job that is the number in full, while the row's digest is of the masked text."""
    if session == "marked":
        PersonalMarks(db.message_store).mark("marked", "/synthetic/docs/bankbook.pdf", "owner")
    db.append_message(session, "user", content=LABELLED)
    [row] = db.get_messages(session)

    assert backfill(db, session, row["id"]) == 1

    reloaded = SessionDB(db_path=tmp_path / "state.db")
    try:
        [row] = reloaded.get_messages(session)
    finally:
        reloaded.close()
    assert row["api_content"] == (SIDECAR.replace(LABELLED, LABELLED_MASKED) if session == "marked" else SIDECAR)


@pytest.mark.parametrize("session", ["marked", "plain"])
def test_lookups_by_content_find_the_row_of_a_marked_job(db, session):
    """The other lookups of this turn's row by its content: the display-kind stamp and held-row coverage."""
    if session == "marked":
        PersonalMarks(db.message_store).mark("marked", "/synthetic/docs/bankbook.pdf", "owner")
    db.append_message(session, "user", content=LABELLED)
    [row] = db.get_messages(session)

    assert db.set_latest_matching_message_display_kind(session, role="user", content=LABELLED, display_kind="note")
    assert db._execute_write(lambda conn: db._matching_active_ids(
        conn, session, {"role": "user", "content": LABELLED})) == [row["id"]]
    assert db.get_messages(session)[0]["display_kind"] == "note"


def test_structured_fields_of_an_unmarked_job_are_stored_unchanged(db):
    _structured_turn(db, "plain")

    user, assistant, tool = db.get_messages("plain")
    assert user["content"] == [{"type": "text", "text": LABELLED}, {"type": "text", "text": {"value": f"번호 {NUMBER}"}}]
    assert json.loads(assistant["reasoning_details"]) == [{"type": "summary", "summary": LABELLED}]
    assert assistant["tool_calls"][0]["function"]["arguments"] == json.dumps({"memo": LABELLED})
    assert tool["content"] == [{"type": "text", "text": f"{LABELLED}, {NUMBER}"}]
