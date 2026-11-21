"""One-shot proposals. Only the stored question and its next owner turn can authorize a rule."""

from __future__ import annotations

from typing import TYPE_CHECKING, NotRequired, TypedDict
from uuid import UUID

from .rule_sentences import interpret

if TYPE_CHECKING:
    from psycopg import Connection


class Offer(TypedDict):
    offer_id: str
    question: str


class Answer(TypedDict):
    status: str
    rule_id: NotRequired[str]


def _uuid(value: str | UUID) -> UUID | None:
    try:
        return UUID(str(value))
    except ValueError:
        return None


def _statement(conn: Connection, rule: UUID | None, pattern: str | None) -> str | None:
    row = conn.execute(
        "SELECT statement, public.lina_current_agent() FROM memory.rule_offer_input(%s, %s)",
        (rule, pattern),
    ).fetchone()
    if not row or not row[0]:
        return None
    text, agent = row
    reading = interpret(text, agent=agent, place=None)
    return text if reading.statement and not reading.refusal and not reading.fact else None


def _offer(conn: Connection, rule: UUID | None, pattern: str | None, session: str | None) -> Offer | None:
    statement = _statement(conn, rule, pattern)
    if statement is None:
        return None
    row = conn.execute(
        "SELECT * FROM memory.make_rule_offer(%s, %s, %s, %s)", (rule, pattern, session, statement)
    ).fetchone()
    return Offer(offer_id=str(row[0]), question=row[1]) if row else None


def shared_offer(conn: Connection, rule_id: str | UUID, session_id: str | None = None) -> Offer | None:
    """Offer matching active per-agent rules, using the source session if omitted."""
    rule = _uuid(rule_id)
    return _offer(conn, rule, None, session_id) if rule else None


def pattern_offer(conn: Connection, pattern_uid: str, session_id: str) -> Offer | None:
    """Internal entry point for a journal candidate identified by the store."""
    return _offer(conn, None, pattern_uid, session_id)


def next_pattern_offer(conn: Connection, session_id: str) -> Offer | None:
    """Select from the calling profile's newest unoffered journal rows, in bounded batches."""
    with conn.transaction(), conn.cursor(name="rule_offer_candidates") as candidates:
        candidates.itersize = 32
        candidates.execute(
            """SELECT h.uid FROM memory.messages h
               WHERE h.agent_id = public.lina_current_agent() AND h.source = 'heart'
                 AND h.forgotten_at IS NULL AND h.grade IN ('owner', 'agent')
                 AND jsonb_typeof(h.native->'note') = 'string' AND cardinality(memory.cited_uids(h.native)) > 0
                 AND NOT EXISTS (SELECT FROM memory.rule_offers o WHERE o.pattern_uid = h.uid)
               ORDER BY h.message_id DESC"""
        )
        for (uid,) in candidates:
            if offer := pattern_offer(conn, uid, session_id):
                return offer
    return None


def answer_offer(conn: Connection, offer_id: str | UUID, internal_owner_uid: str, session_id: str) -> Answer:
    """Recheck policy at birth. Only a retry from the same current owner turn can reuse its receipt."""
    offer = _uuid(offer_id)
    if offer is None:
        return Answer(status="ignored")
    with conn.transaction():
        refs = conn.execute(
            "SELECT anchor_rule, pattern_uid FROM memory.rule_offers WHERE offer_id = %s AND session_id = %s",
            (offer, session_id),
        ).fetchone()
        if not refs:
            return Answer(status="ignored")
        row = conn.execute(
            "SELECT * FROM memory.answer_rule_offer(%s, %s, %s, %s)",
            (offer, internal_owner_uid, session_id, _statement(conn, *refs)),
        ).fetchone()
    if not row:
        return Answer(status="ignored")
    result = Answer(status=row[0])
    if row[1]:
        result["rule_id"] = str(row[1])
    return result
