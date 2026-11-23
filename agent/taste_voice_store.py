"""Per-agent voice budgets and quality measurements in the memory store."""

from __future__ import annotations

from collections.abc import Iterable, Mapping, Sequence
from datetime import datetime, time, timedelta
from hashlib import sha256
from pathlib import Path
from typing import Final
from zoneinfo import ZoneInfo

from psycopg import Cursor
from psycopg.types.json import Jsonb

import hermes_time
from hermes_cli.heart import rows
from hermes_cli.heart.locale import protected_rule
from hermes_cli.heart.taste_context import _prompt_label

_SEOUL: Final = ZoneInfo("Asia/Seoul")


def _lock_agent(cur: Cursor, agent_id: str) -> None:
    """Serialize voice writes for one agent until their transaction ends."""
    cur.execute("SELECT pg_advisory_xact_lock(hashtextextended(%s, 361))", (agent_id,))


def _weekly_changes(
    cur: Cursor,
    agent_id: str,
    monday: datetime,
    now: datetime,
    taste_ids: list[int] | None = None,
) -> list[tuple[int, str]]:
    cur.execute(
        """
        WITH promotions AS (
            SELECT DISTINCT ON (taste_id) taste_id, changed_at, event_id
            FROM memory.taste_events
            WHERE agent_id = %s AND reason = 'status_changed' AND changed_by = 'review'
              AND from_status = 'active' AND to_status = 'core'
              AND changed_at >= %s AND changed_at <= %s
            ORDER BY taste_id, changed_at DESC, event_id DESC
        )
        SELECT t.id, t.label, t.valence
        FROM promotions p JOIN memory.tastes t ON t.id = p.taste_id
        WHERE t.agent_id = %s AND t.status = 'core' AND t.protected_passed IS NOT FALSE
          AND (%s::bigint[] IS NULL OR t.id = ANY(%s::bigint[]))
        ORDER BY p.changed_at DESC, p.event_id DESC
        FOR SHARE OF t
        """,
        (agent_id, monday, now, agent_id, taste_ids, taste_ids),
    )
    changes = []
    for taste_id, label, direction in cur:
        if _prompt_label(label) and protected_rule(label) is None:
            changes.append((taste_id, f"{direction}: {label}"))
            if len(changes) == 3:
                break
    return changes


def _reserve_week(
    cur: Cursor, agent_id: str, turn_id: str, allow_difference: bool
) -> tuple[tuple[str, ...], bool]:
    now = hermes_time.now().astimezone(_SEOUL)
    monday = datetime.combine(now.date() - timedelta(days=now.weekday()), time(), _SEOUL)
    key = monday.date().isoformat()
    cur.execute(
        "SELECT turn_id, spent, taste_ids FROM memory.taste_voice_claims"
        " WHERE agent_id = %s AND scope = 'week' AND budget_key = %s",
        (agent_id, key),
    )
    claim = cur.fetchone()
    if claim is not None:
        owner, spent, taste_ids = claim
        if spent or owner != turn_id:
            return (), False
        if not taste_ids:
            return (), allow_difference
        changes = _weekly_changes(cur, agent_id, monday, now, taste_ids)
        return tuple(line for _, line in changes), False

    changes = _weekly_changes(cur, agent_id, monday, now)
    difference = allow_difference and not changes
    if changes or difference:
        cur.execute(
            "INSERT INTO memory.taste_voice_claims (agent_id, scope, budget_key, turn_id, taste_ids)"
            " VALUES (%s, 'week', %s, %s, %s)"
            " ON CONFLICT (agent_id, scope, budget_key) DO NOTHING RETURNING turn_id",
            (agent_id, key, turn_id, [taste_id for taste_id, _ in changes]),
        )
        if cur.fetchone() is None:
            return (), False
    return tuple(line for _, line in changes), difference


def task_targets(home: Path, picks: Iterable[str]) -> frozenset[str]:
    """Return current task-type labels from the turn's picked taste lines."""
    from agent.taste_voice_policy import _taste_parts

    labels = {
        parts[1] for line in picks
        if (parts := _taste_parts(line)) is not None and protected_rule(parts[1]) is None
    }
    if not labels:
        return frozenset()
    with rows.open_store(home) as store, store._cursor() as cur:
        cur.execute(
            "SELECT label FROM memory.tastes WHERE agent_id = %s AND status = 'active'"
            " AND target_kind = 'task_type' AND protected_passed IS NOT FALSE AND label = ANY(%s)",
            (store.agent_id, sorted(labels)),
        )
        return frozenset(label for (label,) in cur)


def reserve(
    home: Path, turn_id: str, task_key: str | None, *, allow_difference: bool, dislike: bool
) -> tuple[tuple[str, ...], bool, bool]:
    """Claim weekly and task budgets atomically and return the allowed lines."""
    with rows.open_store(home) as store, store.write(), store._writer_cursor() as cur:
        agent_id = store.agent_id
        _lock_agent(cur, agent_id)
        weekly, difference = _reserve_week(cur, agent_id, turn_id, allow_difference)
        task_claimed = False
        if dislike and task_key:
            # The classified task key may itself be a taste label.
            key = sha256(task_key.encode("utf-8")).hexdigest()
            cur.execute(
                "INSERT INTO memory.taste_voice_claims (agent_id, scope, budget_key, turn_id)"
                " VALUES (%s, 'task', %s, %s) ON CONFLICT DO NOTHING",
                (agent_id, key, turn_id),
            )
            cur.execute(
                "SELECT 1 FROM memory.taste_voice_claims"
                " WHERE agent_id = %s AND scope = 'task' AND budget_key = %s"
                " AND turn_id = %s AND NOT spent",
                (agent_id, key, turn_id),
            )
            task_claimed = cur.fetchone() is not None
        return weekly, difference, task_claimed


def settle(home: Path, turn_id: str, *, completed: bool) -> tuple[str, ...]:
    """Spend a completed turn's claims or release its pending claims on failure."""
    with rows.open_store(home) as store, store.write(), store._writer_cursor() as cur:
        agent_id = store.agent_id
        _lock_agent(cur, agent_id)
        weekly = ()
        if completed:
            # A concurrent forget or correction may have retired a pick during the model call.
            cur.execute(
                "SELECT budget_key, taste_ids FROM memory.taste_voice_claims WHERE agent_id = %s"
                " AND turn_id = %s AND scope = 'week' AND NOT spent AND cardinality(taste_ids) > 0",
                (agent_id, turn_id),
            )
            if claim := cur.fetchone():
                start = datetime.fromisoformat(claim[0]).replace(tzinfo=_SEOUL)
                changes = _weekly_changes(cur, agent_id, start, hermes_time.now(), claim[1])
                weekly = tuple(line for _, line in changes)
                if not weekly:
                    cur.execute(
                        "DELETE FROM memory.taste_voice_claims WHERE agent_id = %s AND turn_id = %s"
                        " AND scope = 'week' AND NOT spent",
                        (agent_id, turn_id),
                    )
            cur.execute(
                "UPDATE memory.taste_voice_claims SET spent = true, taste_ids = '{}'"
                " WHERE agent_id = %s AND turn_id = %s AND NOT spent",
                (agent_id, turn_id),
            )
        else:
            cur.execute(
                "DELETE FROM memory.taste_voice_claims"
                " WHERE agent_id = %s AND turn_id = %s AND NOT spent",
                (agent_id, turn_id),
            )
        return weekly


def measure(
    home: Path,
    turn_id: str,
    task_kind: str,
    length: int,
    steps: Sequence[str],
    *,
    disliked: bool,
) -> Mapping[str, int]:
    """Record one sample per turn and compare disliked work with prior samples."""
    from agent.taste_voice_policy import quality_flags

    with rows.open_store(home) as store, store.write(), store._writer_cursor() as cur:
        agent_id = store.agent_id
        _lock_agent(cur, agent_id)
        cur.execute(
            "SELECT flags FROM memory.taste_voice_measurements WHERE agent_id = %s AND turn_id = %s",
            (agent_id, turn_id),
        )
        previous = cur.fetchone()
        if previous is not None:
            return previous[0]

        flags = {}
        if disliked:
            cur.execute(
                "SELECT length, steps FROM memory.taste_voice_measurements WHERE agent_id = %s AND task_kind = %s",
                (agent_id, task_kind),
            )
            flags = quality_flags(length, tuple(steps), cur.fetchall())
        cur.execute(
            "INSERT INTO memory.taste_voice_measurements"
            " (agent_id, turn_id, task_kind, length, steps, disliked, flags) VALUES (%s, %s, %s, %s, %s, %s, %s)",
            (agent_id, turn_id, task_kind, length, list(steps), disliked, Jsonb(flags)),
        )
        return flags
