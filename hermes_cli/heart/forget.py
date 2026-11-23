"""Owner-initiated retirement of one profile's taste."""

from __future__ import annotations

import re
from collections.abc import Sequence
from contextlib import ExitStack
from dataclasses import dataclass, field
from importlib import import_module
from pathlib import Path

from agent.secret_scope import reset_multiplex_context, set_multiplex_context
from hermes_cli.heart import rows, selfnote, storage
from hermes_cli.heart.locale import text
from hermes_cli.heart.taste_context import normalized, target_pattern
from hermes_constants import hermes_home_key, reset_hermes_home_override, set_hermes_home_override
from hermes_state_store import MessageStore

_DEICTIC = re.compile(r"(?:그건|그것(?:은|을)?|그\s*취향(?:은|을)?)\s*(?:잊어|버려)(?:\s*줘)?"
                      r"|(?:please\s+)?forget\s+(?:that|this)(?:\s+taste)?")
_KOREAN = re.compile(r"(?P<label>.+?)\s+취향(?:은|을)?\s*(?:잊어|버려)(?:\s*줘)?")
_ENGLISH = re.compile(r"(?:please\s+)?forget\s+(?:my\s+|the\s+)?(?:taste|preference)\s+for\s+(?P<label>.+)")


@dataclass(frozen=True, slots=True)
class ForgottenTaste:
    taste_id: int
    target_kind: str
    label: str = field(repr=False)
    journal_uids: tuple[str, ...]


@dataclass(frozen=True, slots=True)
class ForgetResult:
    response: str
    forgotten: ForgottenTaste | None = None


def forget_from_conversation(
    request: str, *, owner_verified: bool, history: Sequence[str], target_home: Path | None,
) -> ForgetResult | None:
    """Use the caller's trusted owner verdict, prior conversation text and resolved target home.

    A deictic command refers to the immediately preceding supplied text. Unknown or
    ambiguous targets require clarification. Storage failures propagate for the caller
    to handle without acknowledging success or exposing the target label.
    """
    command = normalized(request).strip().rstrip(".!。！").rstrip()
    explicit = _KOREAN.fullmatch(command) or _ENGLISH.fullmatch(command)
    if not explicit and not _DEICTIC.fullmatch(command):
        return None
    if owner_verified is not True:
        return ForgetResult(text("forget_owner_required"))
    if target_home is None:
        return ForgetResult(text("forget_target_required"))

    with ExitStack() as scope:
        if hermes_home_key(target_home) != hermes_home_key():
            scope.callback(reset_multiplex_context, set_multiplex_context(True))
        scope.callback(reset_hermes_home_override, set_hermes_home_override(target_home))
        store = scope.enter_context(rows.open_store(target_home))
        forgotten = _forget(store, target_home, explicit.group("label") if explicit else None, history)
    if forgotten is None:
        return ForgetResult(text("forget_target_required"))
    return ForgetResult(text("forget_done"), forgotten)


def _forget(
    store: MessageStore, home: Path, explicit_label: str | None, history: Sequence[str],
) -> ForgottenTaste | None:
    context = normalized(history[-1]) if history and explicit_label is None else ""
    agent = store.agent_id
    with store.write(), store._writer_cursor() as cur:
        storage.use_profile_calendar(cur)
        # Review and import take the same lock before reading or changing evidence.
        cur.execute("SELECT pg_advisory_xact_lock(hashtextextended(%s, 357))", (agent,))
        cur.execute("SELECT id, target_kind, label FROM memory.tastes WHERE agent_id=%s AND live ORDER BY id "
                    "FOR NO KEY UPDATE", (agent,))
        candidates = [row for row in cur if (
            normalized(row[2]) == explicit_label if explicit_label is not None else target_pattern(row[2]).search(context)
        )]
        if len(candidates) != 1:
            return None
        taste_id, kind, label = candidates[0]
        cur.execute("SELECT journal_uid FROM memory.taste_evidence WHERE agent_id=%s AND taste_id=%s ORDER BY journal_uid",
                    (agent, taste_id))
        journal_uids = tuple(uid for (uid,) in cur)
        # Evidence inserts take a share lock on their source. Take the exclusive locks
        # before deciding which sources are still shared, so that decision sees their commits.
        cur.execute("SELECT uid FROM memory.messages WHERE agent_id=%s AND source='heart' AND uid=ANY(%s)"
                    " ORDER BY uid FOR UPDATE", (agent, list(journal_uids)))
        cur.fetchall()
        cur.execute("UPDATE memory.tastes SET status='forgotten', forgotten_by='owner' WHERE agent_id=%s AND id=%s",
                    (agent, taste_id))
        cur.execute("""
            UPDATE memory.messages m SET forgotten_at=now()
            WHERE m.agent_id=%s AND m.source='heart' AND m.scope='agent'
              AND m.uid=ANY(%s) AND m.forgotten_at IS NULL
              AND NOT EXISTS (SELECT 1 FROM memory.taste_evidence e
                              WHERE e.agent_id=m.agent_id AND e.journal_uid=m.uid)
        """, (agent, list(journal_uids)))
        # Queue while rollback can still leave the label available for retry.
        # The compactor verifies this taste's committed tombstone before using the marker.
        import_module("plugins.context_engine.lcm-x.taste_forget").queue_taste_forget(
            hermes_home=home, target=label, taste_id=taste_id,
        )
        selfnote.scrub_taste(home, target_pattern(label))
    return ForgottenTaste(taste_id, kind, label, journal_uids)
