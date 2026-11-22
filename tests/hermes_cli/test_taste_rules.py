"""Dated, per-agent scoring and review decisions for #358."""

from dataclasses import replace
from datetime import date, timedelta

import pytest

from hermes_cli.heart.taste_rules import (
    Evidence,
    Taste,
    daily_decisions,
    evidence_weight,
    weekly_decisions,
)


DAY = date(2025, 6, 1)


def ago(days):
    return DAY - timedelta(days=days)


def item(route, weight, days=0):
    return Evidence(ago(days), route, -1 if weight < 0 else 1, abs(weight))


def taste(*items, **changes):
    base = Taste(1, "candidate", "outcome", "indifferent", 0, ago(90), True, items)
    return replace(base, **changes)


def incumbents(count, *, status="active"):
    return [
        taste(item("outcome", 0.15), item("outcome", 0.15), id=i + 10,
              status=status, valence="like", strength=0.3)
        for i in range(count)
    ]


@pytest.mark.parametrize(("route", "direction", "intensity", "expected"), [
    ("exposure", "like", 3, (1, 0.05)),
    ("consistency", "like", 3, (1, 0.05)),
    ("outcome", "like", 1, (1, 0.15)),
    ("outcome", "like", 3, (1, 0.225)),
    ("outcome", "dislike", 3, (-1, 0.225)),
    ("reaction", "like", 3, (1, 0.1)),
    ("reaction", "dislike", 1, (-1, 0.1)),
    ("reaction", "ignored", 3, (-1, 0.03)),
])
def test_journal_sign_and_magnitude(route, direction, intensity, expected):
    assert evidence_weight(route, direction, intensity) == expected


@pytest.mark.parametrize(("evidence", "status", "strength"), [
    ([item("exposure", 0.05)] * 20, "candidate", 0.05),
    ([item("exposure", 0.05, n) for n in range(10)], "candidate", 0.3),
    ([item("consistency", 0.05, 3), item("consistency", 0.05)], "candidate", 0.05 * 0.5**0.1),
    ([item("reaction", 0.10)] * 10, "candidate", 0.4),
    ([item("reaction", 0.10)] * 6 + [item("exposure", 0.05)], "active", 0.4),
    ([item("outcome", 0.15)] * 6, "candidate", 0.8),
    ([item("outcome", 0.15)] * 5 + [item("outcome", 0.15, 1)], "active", 0.8),
])
def test_route_minima_and_strongest_present_cap(evidence, status, strength):
    [change] = daily_decisions([taste(*evidence)], DAY)
    assert change.status == status
    assert change.strength == pytest.approx(strength)


def test_exposure_minimum_can_qualify_a_mixed_record():
    observed = [item("exposure", 0.05, n) for n in range(5)]
    mixed = taste(item("outcome", 0.225), *observed)
    [short] = daily_decisions([mixed], DAY)
    [enough] = daily_decisions([replace(mixed, evidence=(*mixed.evidence, item("exposure", 0.05, 5)))], DAY)
    assert short.strength >= 0.4 and short.status == "candidate"
    assert enough.status == "active"


def test_one_bad_trial_then_two_consecutive_bad_days_then_signed_flip():
    prior = incumbents(3)
    first = taste(item("outcome", -0.225, 1), first_seen=ago(1))
    [single] = daily_decisions([first], ago(1))
    assert (single.status, single.valence, single.strength) == ("candidate", "dislike", 0.3)

    second = replace(first, evidence=(*first.evidence, item("outcome", -0.225)))
    [negative] = daily_decisions([second, *prior], DAY)
    assert (negative.status, negative.valence) == ("active", "dislike")
    assert negative.strength == pytest.approx(0.44486, abs=0.0001)

    good = (item("outcome", 0.225),) * 4
    active = replace(second, status="active", valence="dislike", strength=negative.strength,
                     evidence=(*second.evidence, *good))
    [flip] = daily_decisions([active, *prior], DAY)
    assert (flip.status, flip.valence) == ("active", "like")
    assert flip.strength == pytest.approx(0.9 - negative.strength)


@pytest.mark.parametrize(("age", "followup", "expected"), [
    (13, (), "candidate"),
    (14, (), None),
    (14, (item("reaction", -0.03),), "active"),
    (15, (item("reaction", -0.03),), None),
    (14, (item("reaction", 0.1),), None),
])
def test_one_trial_requires_timely_negative_confirmation(age, followup, expected):
    first = taste(item("outcome", -0.225, age), *followup, first_seen=ago(age))
    [decision] = daily_decisions([first, *incumbents(2)], DAY)
    assert decision.status == expected


@pytest.mark.parametrize(("age", "expected"), [(13, "candidate"), (14, None)])
def test_candidate_expiry_uses_last_observation(age, expected):
    [decision] = daily_decisions([taste(item("outcome", 0.15, age))], DAY)
    assert decision.status == expected


@pytest.mark.parametrize(("status", "age"), [("active", 30), ("core", 60)])
def test_half_life_depends_on_current_status(status, age):
    record = taste(item("outcome", 0.15, age), status=status)
    [change] = daily_decisions([record], DAY)
    assert change.strength == pytest.approx(0.075)
    assert change.status == ("dormant" if status == "active" else "core")


def test_opposing_evidence_can_cancel_to_indifference():
    record = taste(item("outcome", 0.15), item("reaction", -0.1), item("reaction", -0.03),
                   item("reaction", -0.03), item("reaction", -0.03), item("reaction", -0.03),
                   item("reaction", -0.03), item("reaction", 0.1), valence="like", strength=0.25)
    [change] = daily_decisions([record], DAY)
    assert (change.valence, change.strength) == ("indifferent", 0.0)


def test_dormant_waits_for_new_evidence_and_checks_protection_again():
    record = taste(item("outcome", 0.15, 2), item("outcome", 0.15, 2),
                   status="dormant", status_since=ago(1))
    fresh = (*record.evidence, item("outcome", 0.15))
    [old] = daily_decisions([record], DAY)
    [new] = daily_decisions([replace(record, evidence=fresh)], DAY)
    blocked = daily_decisions([replace(record, evidence=fresh, protected_passed=False)], DAY)
    assert (old.status, new.status) == ("dormant", "active")
    assert blocked == []


def test_temperament_is_fixed_and_at_most_three_are_admitted():
    seed = taste(first_seen=DAY, origin="temperament", valence="like", strength=0.3)
    old = [replace(seed, id=i, first_seen=ago(365), status="active") for i in (2, 3)]
    [admitted] = daily_decisions([seed, replace(seed, id=4), *old], DAY)
    assert (admitted.id, admitted.status, admitted.strength) == (1, "active", 0.3)
    assert daily_decisions([replace(seed, protected_passed=False), *old], DAY) == []


def test_daily_dislike_cap_counts_active_members_only():
    trial = taste(item("outcome", -0.225, 1), item("outcome", -0.225))
    [blocked] = daily_decisions([trial, *incumbents(1), replace(incumbents(1)[0], id=9, status="core")], DAY)
    [admitted] = daily_decisions([trial, *incumbents(2)], DAY)
    assert (blocked.status, admitted.status) == ("candidate", "active")


def test_full_active_plus_core_budget_blocks_activation():
    candidate = taste(item("outcome", 0.225), item("outcome", 0.225, 1))
    current = incumbents(12)
    current[-1] = replace(current[-1], status="core")
    [blocked] = daily_decisions([candidate, *current], DAY)
    assert blocked.status == "candidate"


def test_protected_active_member_keeps_its_slot():
    candidate = taste(item("outcome", 0.225), item("outcome", 0.225, 1))
    current = incumbents(12)
    current[-1] = replace(current[-1], evidence=(), protected_passed=False)
    [blocked] = daily_decisions([*current, candidate], DAY)
    assert (blocked.id, blocked.status) == (candidate.id, "candidate")


def test_replaying_weak_confirmation_keeps_the_new_active_dislike_for_that_day():
    trial = taste(item("outcome", -0.225, 14), item("reaction", -0.03))
    [confirmed] = daily_decisions([trial, *incumbents(2)], DAY)
    assert confirmed.status == "active" and confirmed.strength < 0.2
    current = replace(trial, status="active", valence="dislike", strength=confirmed.strength, status_since=DAY)
    assert daily_decisions([current, *incumbents(2)], DAY, changes_today=1) == []


@pytest.mark.parametrize(("used", "admitted"), [(0, [1, 2]), (1, [1]), (2, [])])
def test_daily_change_budget_is_order_independent_and_survives_reruns(used, admitted):
    candidates = [taste(item("outcome", 0.225), item("outcome", 0.225, 1), id=i) for i in (3, 2, 1)]
    changes = daily_decisions(candidates, DAY, changes_today=used)
    assert sorted(c.id for c in changes if c.status == "active") == admitted
    assert all(c.strength >= 0.4 for c in changes)


def test_weekly_promotes_only_after_continuous_active_age_and_up_to_five():
    active = taste(*(item("outcome", 0.15) for _ in range(4)), status="active",
                   status_since=ago(30), valence="like", strength=0.6)
    cores = incumbents(4, status="core")
    eligible = [active, replace(active, id=2), replace(active, id=3, status_since=ago(29))]
    assert daily_decisions(eligible, DAY) == []
    [promoted] = weekly_decisions([*reversed(eligible), *cores], DAY)
    assert (promoted.id, promoted.status) == (1, "core")
    assert weekly_decisions([eligible[-1], replace(active, id=4, status_since=None)], DAY) == []
    below_threshold = replace(active, evidence=(*active.evidence[:-1], item("outcome", 0.15, 5)))
    assert weekly_decisions([below_threshold], DAY) == []
