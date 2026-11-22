"""Import a trial preference ledger and its journal citations into the agent's taste store."""

from __future__ import annotations

import datetime as dt
import hashlib
from collections.abc import Iterator
from dataclasses import dataclass, field
from decimal import Decimal
from pathlib import Path
from typing import Annotated, Literal

from pydantic import Field, ValidationError

from hermes_cli.heart.models import Record
from hermes_state_store import MessageStore

Kind = Literal["topic", "task_type", "tone", "tool", "method", "media", "other"]
Route = Literal["exposure", "outcome", "reaction", "consistency", "temperament"]
Strength = Annotated[Decimal, Field(ge=0, le=1, allow_inf_nan=False)]


class State(Record):
    status: Literal["candidate", "active", "core", "dormant"]
    valence: Literal["like", "dislike", "indifferent"]
    strength: Strength


class Observation(Record):
    line: int = Field(strict=True, ge=1)
    ts: str
    date: dt.date
    route: Route
    sign: Literal[-1, 1]
    weight: Strength
    note: str | None = None


class Event(Record):
    date: dt.date
    before: State | None = Field(alias="from")
    to: State
    reason: str
    by: Literal["agent", "review", "owner", "seed"]

    def values(self, origin: Route, index: int) -> tuple:
        before = self.before
        # Trial seed events are review-authored creations; the taste retains its temperament origin.
        if self.by == "seed" and (origin != "temperament" or index != 0 or before is not None):
            raise LedgerError("history.by: seed requires an initial temperament creation")
        if before is None:
            reason = "created"
        elif self.reason == "correction":
            reason = "correction"
        elif before.status != self.to.status:
            reason = "status_changed"
        elif before.valence != self.to.valence:
            reason = "valence_changed"
        elif before.strength != self.to.strength:
            reason = "strength_changed"
        else:
            raise LedgerError("history: cannot identify a state change")
        return (_midnight(self.date), reason, "review" if self.by == "seed" else self.by,
                before.status if before else None, self.to.status, before.strength if before else None,
                self.to.strength, before.valence if before else None, self.to.valence)


class ProtectedCheck(Record):
    passed: bool
    at: dt.datetime
    rule: str | None = None


class Taste(State):
    id: str
    label: str
    kind: Kind
    origin: Route
    first_seen: dt.date
    protected_check: ProtectedCheck | None = None
    baseline: str | None = None
    evidence: tuple[Observation, ...]
    history: tuple[Event, ...]


class Tombstone(Record):
    id: str
    kind: Kind
    label_hash: str = Field(pattern=r"^[a-fA-F0-9]{64}$")
    date: dt.date
    by: Literal["owner"]


class Ledger(Record):
    version: Literal[1]
    records: list
    tombstones: list
    # These two collections have no typed target. The private ledger file retains them verbatim.
    rejected: list
    expired: list


class LedgerError(ValueError):
    """A source item cannot be faithfully represented by the current store."""


@dataclass
class LedgerResult:
    records: int = 0
    written: int = 0
    present: int = 0
    evidence: int = 0
    events: int = 0
    errors: list[str] = field(default_factory=list)


def _midnight(day: dt.date) -> dt.datetime:
    return dt.datetime.combine(day, dt.time(), tzinfo=dt.UTC)


def _taste_values(item: Taste) -> tuple:
    check = item.protected_check
    at = check.at if check else None
    if at and at.tzinfo is None:
        at = at.replace(tzinfo=dt.UTC)
    return (item.kind, item.label, item.valence, item.strength, item.status, item.origin,
            item.first_seen, item.baseline, check.passed if check else None, at, check.rule if check else None)


TASTE = "target_kind,label,valence,strength,status,origin,first_seen,baseline,protected_passed,protected_at,protected_rule"
EVIDENCE = "journal_uid,message_uids,observed_on,route,sign,weight,note"
EVENT = "changed_at,reason,changed_by,from_status,to_status,from_strength,to_strength,from_valence,to_valence"


def _observations(cur, item: Taste, journal: dict[int, tuple[str, str]], dry_run: bool) -> list[tuple]:
    evidence = []
    for obs in item.evidence:
        ref = journal.get(obs.line)
        if ref is None or ref[1] != obs.ts:
            raise LedgerError(f"evidence.line {obs.line}: journal line missing or timestamp differs")
        uid = ref[0]
        cur.execute("SELECT native->'evidence' FROM memory.messages WHERE uid=%s AND source='heart'"
                    " AND agent_id=public.lina_current_agent() AND forgotten_at IS NULL", (uid,))
        row = cur.fetchone()
        if row is None and not dry_run:
            raise LedgerError(f"evidence.line {obs.line}: journal row unavailable")
        evidence.append((uid, row[0] if row else [], obs.date, obs.route, obs.sign, obs.weight, obs.note))
    return sorted(evidence, key=lambda observation: observation[0])


def _children(cur, tid: int, table: str, columns: str, order: str, values: list[tuple], existing: bool) -> None:
    if existing:
        cur.execute(f"SELECT {columns} FROM memory.{table} WHERE agent_id=public.lina_current_agent()"
                    f" AND taste_id=%s ORDER BY {order}", (tid,))
        if cur.fetchall() != values:
            raise LedgerError(f"{table}: existing rows differ from trial")
    elif values:
        params = ",".join(["%s"] * (len(values[0]) + 1))
        cur.executemany(f"INSERT INTO memory.{table}(agent_id,taste_id,{columns})"
                        f" VALUES(public.lina_current_agent(),{params})", [(tid, *v) for v in values])


def _taste(cur, item: Taste, journal: dict[int, tuple[str, str]], dry_run: bool) -> bool:
    if item.origin == "temperament" and item.strength != Decimal("0.3"):
        raise LedgerError("temperament strength must be 0.3")
    evidence = _observations(cur, item, journal, dry_run)
    events = [event.values(item.origin, index) for index, event in enumerate(item.history)]
    values = _taste_values(item)
    cur.execute(f"SELECT id,{TASTE} FROM memory.tastes WHERE agent_id=public.lina_current_agent()"
                " AND target_kind=%s AND label_hash=%s AND live FOR UPDATE",
                (item.kind, hashlib.sha256(item.label.encode()).digest()))
    existing = cur.fetchone()
    if existing:
        tid = existing[0]
        if existing[1:] != values:
            raise LedgerError("existing taste differs from trial")
    elif dry_run:
        return True
    else:
        cur.execute(f"INSERT INTO memory.tastes(agent_id,{TASTE}) VALUES(public.lina_current_agent(),"
                    "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s) RETURNING id", values)
        tid, = cur.fetchone()
    _children(cur, tid, "taste_evidence", EVIDENCE, "journal_uid", evidence, bool(existing))
    _children(cur, tid, "taste_events", EVENT, "event_id", events, bool(existing))
    return existing is None


def _tombstone(cur, item: Tombstone, dry_run: bool) -> bool:
    identity = (item.kind, bytes.fromhex(item.label_hash), item.date)
    cur.execute("SELECT id FROM memory.tastes WHERE agent_id=public.lina_current_agent() AND target_kind=%s"
                " AND label_hash=%s AND forgotten_on=%s", identity)
    if cur.fetchone():
        return False
    if not dry_run:
        # The forget UPDATE stamps today. A trial tombstone already has its authoritative date.
        cur.execute("INSERT INTO memory.tastes(agent_id,target_kind,label_hash,forgotten_on,first_seen,status,"
                    "forgotten_by) VALUES(public.lina_current_agent(),%s,%s,%s,%s,'forgotten','owner')",
                    (*identity, item.date))
    return True


def _record_error(exc: ValidationError) -> list[str]:
    return [f"{'.'.join(map(str, e['loc'])) or 'preferences.json'}: {e['msg']}"
            for e in exc.errors(include_input=False, include_url=False)]


def _items(ledger: Ledger, errors: list[str]) -> Iterator[tuple[str, Taste | Tombstone]]:
    seen_ids: set[str] = set()
    seen_targets: set[tuple] = set()
    for group, model, entries in (("tombstones", Tombstone, ledger.tombstones), ("records", Taste, ledger.records)):
        for index, raw in enumerate(entries):
            location = f"{group}[{index}]"
            try:
                item = model.model_validate(raw)
            except ValidationError as exc:
                errors.extend(f"{location}.{error}" for error in _record_error(exc))
                continue
            target = ((item.kind, item.label) if isinstance(item, Taste)
                      else (item.kind, item.label_hash.lower(), item.date))
            if item.id in seen_ids or target in seen_targets:
                errors.append(f"{location}: duplicate trial id or target")
                continue
            seen_ids.add(item.id)
            seen_targets.add(target)
            yield location, item


def import_ledger(path: Path, store: MessageStore, journal: dict[int, tuple[str, str]], *, dry_run: bool) -> LedgerResult:
    import psycopg

    result = LedgerResult()
    try:
        ledger = Ledger.model_validate_json(path.read_bytes())
    except ValidationError as exc:
        result.errors.extend(_record_error(exc))
        return result

    result.records = len(ledger.records) + len(ledger.tombstones)
    try:
        with store.write(), store._writer_cursor() as cur:
            # A second importer for the same agent must see the committed event log before comparing it.
            cur.execute("SELECT pg_advisory_xact_lock(hashtextextended(%s, 357))", (store.agent_id,))
            for location, item in _items(ledger, result.errors):
                try:
                    # A savepoint lets us report subsequent bad records after a database constraint fails.
                    with cur.connection.transaction():
                        if isinstance(item, Tombstone):
                            inserted = _tombstone(cur, item, dry_run)
                        else:
                            inserted = _taste(cur, item, journal, dry_run)
                            result.evidence += len(item.evidence)
                            result.events += len(item.history)
                        result.written += int(inserted)
                        result.present += int(not inserted)
                except (LedgerError, psycopg.IntegrityError, psycopg.DataError) as exc:
                    reason = str(exc) if isinstance(exc, LedgerError) else exc.diag.constraint_name or exc.sqlstate
                    result.errors.append(f"{location}: {reason}")
            if result.errors:
                raise LedgerError("roll back the whole ledger")
    except LedgerError:
        result.written = result.present = result.evidence = result.events = 0
    return result
