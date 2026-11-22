"""Transactional daily refinement and weekly core promotion for one agent."""

from __future__ import annotations

import datetime as dt
import logging
import time
from collections import Counter, defaultdict

from pydantic import ValidationError

from hermes_cli.heart import rows, storage
from hermes_cli.heart.ledger import EVENT
from hermes_cli.heart.locale import protected_rule
from hermes_cli.heart.models import Entry
from hermes_cli.heart.parser import ROUTES, TARGET_KINDS
from hermes_cli.heart.taste_rules import Decision, Evidence, Taste, daily_decisions, evidence_weight, weekly_decisions
from hermes_state_store import MessageStore
from hermes_time import get_timezone

logger = logging.getLogger(__name__)

# Used both when accepting a new citation and when counting it on a later review.
_OWN_TURN = """
    m.source='agent' AND m.scope='agent' AND m.forgotten_at IS NULL AND m.session_id IS NOT NULL
    AND (m.speaker <> 'agent' OR m.speaker_ref='agent:' || m.agent_id)
    AND (m.speaker <> 'outside' OR m.role IS DISTINCT FROM 'user')
    AND NOT coalesce(starts_with(m.author, 'agent:') AND m.author <> 'agent:' || m.agent_id, false)
    AND m.native->>'_compressed_summary' IS DISTINCT FROM 'true'
"""
_OWN_JOURNAL = """
    j.source='heart' AND j.scope='agent' AND j.forgotten_at IS NULL
    AND j.speaker='agent' AND j.speaker_ref='agent:' || j.agent_id
    AND NOT coalesce(starts_with(j.author, 'agent:') AND j.author <> j.speaker_ref, false)
"""


def _journals(cur, agent: str, day: dt.date):
    cur.execute(f"""
        SELECT j.uid, j.native, EXISTS (
            SELECT 1 FROM memory.taste_consumed c WHERE c.agent_id=j.agent_id AND c.journal_uid=j.uid
        ) FROM memory.messages j
        WHERE agent_id=%s AND {_OWN_JOURNAL} AND ts >= %s AND ts < %s AND native->'pref' IS NOT NULL
        ORDER BY ts, message_id FOR SHARE
    """, (agent, storage.midnight(day), storage.midnight(day + dt.timedelta(days=1))))
    return cur.fetchall()


def _citations(cur, agent: str, entry: Entry, day: dt.date) -> bool:
    cited = set(entry.evidence)
    if not cited:
        return False
    cur.execute(f"""
        SELECT uid,speaker FROM memory.messages m
        WHERE uid=ANY(%s) AND agent_id=%s AND {_OWN_TURN} AND ts >= %s AND ts < %s FOR SHARE
    """, (list(cited), agent, storage.midnight(day), storage.midnight(day + dt.timedelta(days=1))))
    found = dict(cur.fetchall())
    return cited == found.keys() and (not entry.reaction or "owner" in found.values())


def _event(cur, agent: str, tid: int, day: dt.date, reason: str, before: tuple, after: tuple) -> None:
    cur.execute(f"INSERT INTO memory.taste_events(agent_id,taste_id,{EVENT}) "
                "VALUES (%s,%s,%s,%s,'review',%s,%s,%s,%s,%s,%s)",
                (agent, tid, storage.midnight(day), reason, before[0], after[0], before[1], after[1], before[2], after[2]))


def _observe(cur, agent: str, day: dt.date, uid: str, entry: Entry) -> str:
    pref = entry.pref
    if (pref is None or pref.kind not in TARGET_KINDS or pref.route not in ROUTES
            or not pref.target.strip() or pref.target != pref.target.strip() or len(pref.target) > 80
            or pref.dir not in ("like", "dislike", "ignored") or entry.strength not in (1, 2, 3)
            or (pref.dir == "ignored" and pref.route != "reaction")
            or (pref.route in ("consistency", "exposure") and pref.dir != "like")):
        return "invalid-pref"
    if rule := protected_rule(pref.target):
        return rule
    if not _citations(cur, agent, entry, day):
        return "citation"
    if pref.route == "consistency":
        cur.execute("SELECT 1 FROM memory.tastes WHERE agent_id=%s AND status='active' LIMIT 1", (agent,))
        if entry.via != "review" or not cur.fetchone():
            return "consistency-source"
    cur.execute("SELECT id FROM memory.tastes WHERE agent_id=%s AND target_kind=%s AND label=%s AND live",
                (agent, pref.kind, pref.target))
    existing = cur.fetchone()
    if existing:
        tid, = existing
    else:
        # The exclusion constraint also covers the owner's forgotten-target reservation.
        cur.execute("INSERT INTO memory.tastes(agent_id,target_kind,label,status,valence,strength,origin,first_seen,"
                    "protected_passed,protected_at) VALUES (%s,%s,%s,'candidate','indifferent',0,%s,%s,true,%s)"
                    " ON CONFLICT DO NOTHING RETURNING id",
                    (agent, pref.kind, pref.target, pref.route, day, storage.midnight(day)))
        if not (created := cur.fetchone()):
            return "reserved-target"
        tid, = created
        _event(cur, agent, tid, day, "created", (None, None, None), ("candidate", 0, "indifferent"))
    if pref.route in ("exposure", "consistency"):
        cur.execute("SELECT 1 FROM memory.taste_evidence WHERE agent_id=%s AND taste_id=%s AND route=%s"
                    " AND (route='consistency' OR observed_on=%s) LIMIT 1", (agent, tid, pref.route, day))
        if cur.fetchone():
            return "present"
    sign, weight = evidence_weight(pref.route, pref.dir, entry.strength)
    cur.execute("INSERT INTO memory.taste_evidence(agent_id,taste_id,journal_uid,message_uids,observed_on,"
                "route,sign,weight) VALUES (%s,%s,%s,%s,%s,%s,%s,%s) ON CONFLICT DO NOTHING",
                (agent, tid, uid, list(entry.evidence), day, pref.route, sign, weight))
    return "written" if cur.rowcount else "present"


def _tastes(cur, agent: str, day: dt.date) -> list[Taste]:
    cur.execute(f"""
        SELECT e.taste_id,e.observed_on,e.route,e.sign,e.weight FROM memory.taste_evidence e
        JOIN memory.messages j ON j.uid=e.journal_uid AND j.agent_id=e.agent_id
        WHERE e.agent_id=%s AND e.observed_on<=%s AND {_OWN_JOURNAL}
          AND NOT EXISTS (
            SELECT 1 FROM unnest(e.message_uids) cited(uid) WHERE NOT EXISTS (
              SELECT 1 FROM memory.messages m WHERE m.uid=cited.uid AND m.agent_id=e.agent_id AND {_OWN_TURN}))
        ORDER BY e.taste_id,e.observed_on,j.ts,j.message_id
    """, (agent, day))
    items = defaultdict(list)
    for tid, observed, route, sign, weight in cur.fetchall():
        items[tid].append(Evidence(observed, route, sign, float(weight)))
    cur.execute("""
        SELECT t.id,t.status,t.origin,t.valence,t.strength,t.first_seen,t.label,
               (SELECT max(e.changed_at) FROM memory.taste_events e
                 WHERE e.agent_id=t.agent_id AND e.taste_id=t.id AND e.to_status=t.status
                   AND e.from_status IS DISTINCT FROM e.to_status)
        FROM memory.tastes t WHERE agent_id=%s AND live AND first_seen<=%s ORDER BY id FOR UPDATE
    """, (agent, day))
    return [Taste(tid, status, origin, valence, float(strength), first, protected_rule(label) is None,
                  tuple(items[tid]), storage.profile_date(since) if since else first)
            for tid, status, origin, valence, strength, first, label, since in cur.fetchall()]


def _apply(cur, agent: str, day: dt.date, taste: Taste, decision: Decision) -> None:
    before = (taste.status, taste.strength, taste.valence)
    after = (decision.status, decision.strength, decision.valence) if decision.status else (None, None, None)
    for idx, reason in enumerate(("status_changed", "strength_changed", "valence_changed")):
        if before[idx] != after[idx] and (decision.status or idx == 0):
            _event(cur, agent, taste.id, day, reason, before, after)
    if decision.status is None:
        # A one-trial dislike can expire even if it received positive evidence in the meantime.
        cur.execute("DELETE FROM memory.taste_evidence WHERE agent_id=%s AND taste_id=%s", (agent, taste.id))
        cur.execute("DELETE FROM memory.tastes WHERE agent_id=%s AND id=%s", (agent, taste.id))
    else:
        cur.execute("UPDATE memory.tastes SET status=%s,strength=%s,valence=%s,protected_passed=true,"
                    "protected_at=%s,protected_rule=NULL WHERE agent_id=%s AND id=%s",
                    (*after, storage.midnight(day), agent, taste.id))


def review(store: MessageStore, day: dt.date, *, weekly: bool = False) -> Counter:
    agent = store.agent_id
    counts = Counter()
    with store.write(), store._writer_cursor() as cur:
        # CURRENT_DATE in the taste constraints must use the same calendar as the review.
        zone = get_timezone()
        db_zone = zone.key if zone else f"INTERVAL '{time.localtime().tm_gmtoff} seconds'"
        cur.execute("SELECT set_config('TimeZone', %s, true)", (db_zone,))
        # Share the importer's per-agent lock. Evidence, history and the daily budget commit together.
        cur.execute("SELECT pg_advisory_xact_lock(hashtextextended(%s, 357))", (agent,))
        tomorrow = storage.midnight(day + dt.timedelta(days=1))
        cur.execute("SELECT 1 FROM memory.taste_events WHERE agent_id=%s AND changed_at >= %s LIMIT 1",
                    (agent, tomorrow))
        if cur.fetchone():
            return Counter({"later-review": 1})
        if not weekly:
            for uid, native, consumed in _journals(cur, agent, day):
                if consumed:
                    counts["consumed"] += 1
                    continue
                try:
                    entry = Entry.model_validate(native)
                except ValidationError:
                    result = "invalid-pref"
                else:
                    result = _observe(cur, agent, day, uid, entry)
                counts[result] += 1
                if result in {"written", "present"}:
                    cur.execute("INSERT INTO memory.taste_consumed(agent_id,journal_uid,consumed_on)"
                                " VALUES (%s,%s,%s) ON CONFLICT DO NOTHING", (agent, uid, day))
                else:
                    logger.info("Taste evidence refused: journal=%s rule=%s", uid, result)
        tastes = _tastes(cur, agent, day)
        if weekly:
            decisions = weekly_decisions(tastes, day)
        else:
            cur.execute("SELECT count(*) FROM memory.taste_events WHERE agent_id=%s AND reason='status_changed' "
                        "AND changed_at >= %s AND changed_at < %s AND to_status IS DISTINCT FROM 'core'",
                        (agent, storage.midnight(day), tomorrow))
            decisions = daily_decisions(tastes, day, changes_today=cur.fetchone()[0])
        by_id = {taste.id: taste for taste in tastes}
        for decision in decisions:
            taste = by_id[decision.id]
            _apply(cur, agent, day, taste, decision)
            counts["updated"] += 1
    return counts


def daily(args) -> None:
    day = storage.review_day(args.date)
    with rows.open_store(storage.home()) as store:
        counts = review(store, day)
    print(" ".join(f"{rule}={count}" for rule, count in sorted(counts.items())) or "updated=0")
