"""Pure review decisions for one agent's live tastes and validated evidence."""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass, replace
from datetime import date
from math import fsum


@dataclass(frozen=True, slots=True)
class Evidence:
    observed_on: date
    route: str
    sign: int
    weight: float


@dataclass(frozen=True, slots=True)
class Taste:
    id: int
    status: str
    origin: str
    valence: str
    strength: float
    first_seen: date
    protected_passed: bool
    evidence: tuple[Evidence, ...] = ()
    status_since: date | None = None


@dataclass(frozen=True, slots=True)
class Decision:
    """The new stored state. A null status means delete the expired candidate."""

    id: int
    status: str | None
    valence: str
    strength: float


_CAP = {"exposure": 0.3, "outcome": 0.8, "reaction": 0.4, "consistency": 0.2,
        "temperament": 0.3}


def evidence_weight(route: str, direction: str, intensity: int) -> tuple[int, float]:
    """Translate one explicit journal observation to the store's sign and magnitude."""
    match route, direction:
        case ("exposure" | "consistency"), "like":
            return 1, 0.05
        case "outcome", ("like" | "dislike"):
            return (1 if direction == "like" else -1), (0.225 if intensity == 3 else 0.15)
        case "reaction", ("like" | "dislike"):
            return (1 if direction == "like" else -1), 0.10
        case "reaction", "ignored":
            return -1, 0.03
        case "temperament", ("like" | "dislike"):
            return (1 if direction == "like" else -1), 0.3
        case _:
            raise ValueError(f"Unsupported taste observation: {route}/{direction}")


def _observations(taste: Taste, day: date) -> list[Evidence]:
    """Count the first consistency inference ever and one exposure per day."""
    seen: set[tuple[str, date | None]] = set()
    items = []
    for item in sorted(taste.evidence, key=lambda row: row.observed_on):
        if item.observed_on > day:
            break
        if item.route in {"exposure", "consistency"}:
            key = (item.route, item.observed_on if item.route == "exposure" else None)
            if key in seen:
                continue
            seen.add(key)
        items.append(item)
    return items


def _meets_minimum(items: list[Evidence]) -> bool:
    exposure_days = {item.observed_on for item in items if item.route == "exposure"}
    outcome_days = {item.observed_on for item in items if item.route == "outcome"}
    routes = {item.route for item in items}
    return len(exposure_days) >= 6 or len(outcome_days) >= 2 or ("reaction" in routes and len(routes) > 1)


def _candidate(taste: Taste, scored: Decision, items: list[Evidence], day: date) -> Decision:
    latest = max(taste.first_seen, items[-1].observed_on) if items else taste.first_seen
    if (day - latest).days >= 14:
        return replace(scored, status=None)
    if items and taste.origin == "outcome":
        first = items[0]
        if first.route == "outcome" and first.sign < 0 and first.weight == 0.225:
            age = (day - first.observed_on).days
            confirmed = any(row.sign < 0 and (row.observed_on - first.observed_on).days <= 14
                            for row in items[1:])
            if not confirmed:
                if age >= 14:
                    return replace(scored, status=None)
                if len(items) == 1:
                    return replace(scored, strength=round(0.3 * 0.5 ** (age / 30), 12))
                return scored
            if scored.valence == "dislike":
                return replace(scored, status="active")
    if scored.strength >= 0.4 and _meets_minimum(items):
        return replace(scored, status="active")
    return scored


def _daily_proposal(taste: Taste, day: date) -> Decision:
    if taste.origin == "temperament":
        status = "active" if taste.status == "candidate" else taste.status
        return Decision(taste.id, status, taste.valence, 0.3)

    items = _observations(taste, day)
    half_life = 60 if taste.status == "core" else 30
    score = fsum(row.sign * row.weight * 0.5 ** ((day - row.observed_on).days / half_life) for row in items)
    # Normalize floating point cancellation before comparing signs or storing a change.
    score = round(score, 12)
    valence = "like" if score > 0 else "dislike" if score < 0 else "indifferent"
    cap = max((_CAP[row.route] for row in items), default=0.0)
    scored = Decision(taste.id, taste.status, valence if items else taste.valence, min(abs(score), cap))
    match taste.status:
        case "candidate":
            return _candidate(taste, scored, items, day)
        case "active" if scored.strength < 0.2 and taste.status_since != day:
            return replace(scored, status="dormant")
        case "dormant" if scored.strength >= 0.2 and items:
            if taste.status_since is not None and items[-1].observed_on > taste.status_since:
                return replace(scored, status="active")
    return scored


def _state(taste: Taste) -> Decision:
    return Decision(taste.id, taste.status, taste.valence, taste.strength)


def _fits(roster: dict[int, Decision], decision: Decision) -> bool:
    """Count the post-change roster, including changes to the active denominator."""
    members = [row for tid, row in roster.items() if tid != decision.id]
    if decision.status in {"active", "core"}:
        members.append(decision)
    active = [row for row in members if row.status == "active"]
    return (len(members) <= 12 and len(members) - len(active) <= 5
            and 3 * sum(row.valence == "dislike" for row in active) <= len(active))


def _priority(pair: tuple[Taste, Decision]) -> tuple[int, bool, float, int]:
    taste, decision = pair
    # Release slots first, then account for existing members before admitting candidates.
    if decision.status in {None, "dormant"} and decision.status != taste.status:
        return (0, taste.valence != "dislike", decision.strength, taste.id)
    return (1 if taste.status == "active" else 2, decision.valence == "dislike", -decision.strength, taste.id)


def daily_decisions(tastes: Sequence[Taste], day: date, *, changes_today: int = 0) -> list[Decision]:
    """Score all live rows. Supply the count of daily transitions already persisted on day."""
    rows = [row for row in tastes if row.status != "forgotten" and row.first_seen <= day]
    roster = {row.id: _state(row) for row in rows if row.status in {"active", "core"}}
    seeds = sum(row.id in roster and row.origin == "temperament" for row in rows)
    changes = []
    proposals = [(row, _daily_proposal(row, day)) for row in rows if row.protected_passed]
    remaining = max(0, 2 - changes_today)
    for row, proposed in sorted(proposals, key=_priority):
        decision = proposed
        seed = row.origin == "temperament" and row.status == "candidate" and decision.status == "active"
        if remaining == 0 or (seed and seeds >= 3):
            decision = replace(decision, status=row.status)
        if not _fits(roster, decision):
            decision = replace(decision, status=row.status)
            if not _fits(roster, decision):
                # A signed flip can itself exhaust the active dislike quota.
                if row.status != "active" or remaining == 0:
                    continue
                decision = replace(decision, status="dormant")
                if not _fits(roster, decision):
                    continue
        if decision == _state(row):
            continue
        if decision.status != row.status:
            remaining -= 1
            seeds += int(seed and decision.status == "active")
        if decision.status in {"active", "core"}:
            roster[row.id] = decision
        else:
            roster.pop(row.id, None)
        changes.append(decision)
    return changes


def weekly_decisions(tastes: Sequence[Taste], day: date) -> list[Decision]:
    """Promote eligible active rows using the latest daily scores and continuous active age."""
    roster = {row.id: _state(row) for row in tastes if row.status in {"active", "core"}}
    eligible = [row for row in tastes if row.status == "active" and row.protected_passed
                and row.strength >= 0.6 and row.status_since is not None and (day - row.status_since).days >= 30]
    changes = []
    for row in sorted(eligible, key=lambda row: (row.valence != "dislike", -row.strength, row.id)):
        decision = replace(_state(row), status="core")
        if _fits(roster, decision):
            roster[row.id] = decision
            changes.append(decision)
    return changes
