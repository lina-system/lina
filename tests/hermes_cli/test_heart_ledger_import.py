"""Trial preference ledgers imported through the public heart command."""

import json
from datetime import date, timedelta
from decimal import Decimal
from hashlib import sha256
from pathlib import Path

import pytest

from tests.hermes_cli.test_heart_import import MAPPED, heart_rows, line, moved_lcm
from tests.hermes_cli.test_heart_journal import invoke, sandbox

__all__ = ["moved_lcm", "sandbox"]

SEED_DAY = "2026-11-09"
REVIEW_DAY = "2026-11-10"
LABEL = "Sorting sample swatches"
SEED = {"status": "active", "strength": 0.3, "valence": "like"}
REVIEWED = {**SEED, "valence": "dislike"}
TOMBSTONE_LABEL = "Grouping sample tiles"


def _temperament(label: str = LABEL, day: str = SEED_DAY):
    return {
        "id": "p001",
        "kind": "method",
        "label": label,
        "norm": label.lower(),
        **SEED,
        "origin": "temperament",
        "first_seen": day,
        "active_since": day,
        "last_evidence": day,
        "fast_dislike": False,
        "baseline": "like",
        "protected_check": {"passed": True, "at": f"{day}T09:00:00+09:00", "rule": None},
        "evidence": [],
        "daily": [{"date": day, "status": "active", "strength": 0.3}],
        "history": [{"date": day, "from": None, "to": SEED, "reason": "created", "by": "seed"}],
    }


def _reviewed_temperament():
    record = {**_temperament(), **REVIEWED, "last_evidence": REVIEW_DAY}
    record["history"].append(
        {"date": REVIEW_DAY, "from": SEED, "to": REVIEWED, "reason": "valence_changed", "by": "review"},
    )
    record["daily"].append({"date": REVIEW_DAY, "status": "active", "strength": 0.3})
    record["evidence"].append({
        "line": 2, "ts": f"{REVIEW_DAY}T23:40:00+09:00", "date": REVIEW_DAY,
        "route": "outcome", "sign": -1, "weight": 0.15, "intensity": 1,
    })
    return record


def _source(root: Path, *, records=(), tombstones=(), created: str = SEED_DAY, updated: str = REVIEW_DAY) -> Path:
    heart = root / "heart"
    heart.mkdir(parents=True)
    journal = [
        line("Synthetic comparison", [], via="turn"),
        line("Synthetic swatch trial", [MAPPED], pref={
            "target": LABEL, "kind": "method", "dir": "dislike", "route": "outcome",
        }),
    ]
    (heart / "journal.jsonl").write_text(
        "".join(json.dumps(entry) + "\n" for entry in journal), encoding="utf-8",
    )
    (heart / "preferences.json").write_text(json.dumps({
        "version": 1, "agent": "alpha", "created": created, "updated": updated,
        "journal_seen": len(journal), "next_id": 5, "records": list(records),
        "rejected": [], "tombstones": list(tombstones), "expired": [],
    }), encoding="utf-8")
    return root


def _tombstone(forgotten: date):
    return {
        "id": "p004", "kind": "method", "by": "owner", "date": forgotten.isoformat(),
        "label_hash": sha256(TOMBSTONE_LABEL.encode()).hexdigest(),
    }


def _ledger_rows(sql):
    return (
        sql("SELECT * FROM memory.tastes ORDER BY agent_id, id"),
        sql("SELECT * FROM memory.taste_evidence ORDER BY agent_id, taste_id, journal_uid"),
        sql("SELECT * FROM memory.taste_events ORDER BY agent_id, taste_id, event_id"),
    )


def _assert_empty_ledger(sql) -> None:
    assert _ledger_rows(sql) == ([], [], [])


def test_import_restores_taste_history_and_links_the_cited_journal_line(
    sandbox, tmp_path, moved_lcm, memory_store_sql,
):
    source = _source(tmp_path / "trial", records=[_reviewed_temperament()])

    code, output, error = invoke("import", "--from", str(source))

    assert code == 0, output + error
    tastes = memory_store_sql(
        "SELECT id, target_kind, label, valence, strength, status, origin, first_seen, protected_passed "
        "FROM memory.tastes WHERE agent_id = 'alpha'",
    )
    assert tastes, "heart import did not write the trial taste"
    [(taste_id, *_)] = tastes
    assert tastes == [(
        taste_id, "method", LABEL, "dislike", Decimal("0.3"), "active",
        "temperament", date.fromisoformat(SEED_DAY), True,
    )]
    journal = {native["event"]: uid for uid, _, _, native in heart_rows(memory_store_sql)}
    assert journal.keys() == {"Synthetic comparison", "Synthetic swatch trial"}
    assert memory_store_sql(
        "SELECT taste_id, journal_uid, message_uids, observed_on, route, sign, weight "
        "FROM memory.taste_evidence WHERE agent_id = 'alpha'",
    ) == [(
        taste_id, journal["Synthetic swatch trial"], [moved_lcm], date.fromisoformat(REVIEW_DAY),
        "outcome", -1, Decimal("0.15"),
    )]
    assert memory_store_sql(
        "SELECT taste_id, (changed_at AT TIME ZONE 'UTC')::date, reason, changed_by, "
        "from_status, to_status, from_strength, to_strength, from_valence, to_valence "
        "FROM memory.taste_events WHERE agent_id = 'alpha' ORDER BY event_id",
    ) == [
        (taste_id, date.fromisoformat(SEED_DAY), "created", "review", None, "active",
         None, Decimal("0.3"), None, "like"),
        (taste_id, date.fromisoformat(REVIEW_DAY), "valence_changed", "review", "active", "active",
         Decimal("0.3"), Decimal("0.3"), "like", "dislike"),
    ]


def test_reimport_keeps_the_same_taste_evidence_and_events(sandbox, tmp_path, moved_lcm, memory_store_sql):
    source = _source(tmp_path / "trial", records=[_reviewed_temperament()])
    code, output, error = invoke("import", "--from", str(source))
    assert code == 0, output + error
    before = _ledger_rows(memory_store_sql)
    assert tuple(map(len, before)) == (1, 1, 2)

    code, output, error = invoke("import", "--from", str(source))

    assert code == 0, output + error
    assert _ledger_rows(memory_store_sql) == before


def test_import_preserves_a_historical_scrubbed_tombstone(sandbox, tmp_path, moved_lcm, memory_store_sql):
    forgotten = date(2026, 11, 10)
    source = _source(tmp_path / "trial", tombstones=[_tombstone(forgotten)])

    code, output, error = invoke("import", "--from", str(source))

    assert code == 0, output + error
    tombstones = memory_store_sql(
        "SELECT id, target_kind, label_hash, forgotten_on, forgotten_by, status, label, strength, valence, origin "
        "FROM memory.tastes WHERE agent_id = 'alpha'",
    )
    assert tombstones, "heart import did not write the trial tombstone"
    [(taste_id, *_)] = tombstones
    assert taste_id > 0
    assert tombstones == [(
        taste_id, "method", sha256(TOMBSTONE_LABEL.encode()).digest(),
        forgotten, "owner", "forgotten", None, None, None, None,
    )]
    assert memory_store_sql("SELECT taste_id FROM memory.taste_evidence") == []
    code, output, error = invoke("import", "--from", str(source))
    assert code == 0, output + error
    assert memory_store_sql("SELECT id FROM memory.tastes WHERE agent_id = 'alpha'") == [(taste_id,)]


@pytest.mark.parametrize("age, expected_code", [(89, 1), (90, 0)], ids=["89-days-blocked", "90-days-allowed"])
def test_import_respects_the_forgotten_target_cooldown(
    sandbox, tmp_path, moved_lcm, memory_store_sql, age, expected_code,
):
    [(today,)] = memory_store_sql("SELECT CURRENT_DATE")
    forgotten = today - timedelta(days=age)
    source = _source(
        tmp_path / "trial", records=[_temperament(TOMBSTONE_LABEL, today.isoformat())],
        tombstones=[_tombstone(forgotten)], created=forgotten.isoformat(), updated=today.isoformat(),
    )

    code, output, error = invoke("import", "--from", str(source))

    assert code == expected_code, output + error
    if expected_code:
        _assert_empty_ledger(memory_store_sql)
    else:
        assert memory_store_sql(
            "SELECT status, label, forgotten_on FROM memory.tastes "
            "WHERE agent_id = 'alpha' ORDER BY forgotten_on NULLS LAST",
        ) == [("forgotten", None, forgotten), ("active", TOMBSTONE_LABEL, None)]


@pytest.mark.parametrize("stored", [True, False], ids=["stored", "pending"])
@pytest.mark.parametrize("age, expected_code", [(89, 1), (90, 0)])
def test_dry_run_matches_cooldown_import(sandbox, tmp_path, memory_store_sql, stored, age, expected_code):
    [(today,)] = memory_store_sql("SELECT CURRENT_DATE")
    forgotten = today - timedelta(days=200)
    first_seen = (forgotten + timedelta(days=age)).isoformat()
    tombstone = _tombstone(forgotten)
    if stored:
        initial = _source(tmp_path / "initial", tombstones=[tombstone])
        assert invoke("import", "--from", str(initial))[0] == 0
    source = _source(tmp_path / "trial", records=[_temperament(TOMBSTONE_LABEL, first_seen)],
                     tombstones=[] if stored else [tombstone])

    before = _ledger_rows(memory_store_sql)
    preview, preview_out, _ = invoke("import", "--from", str(source), "--dry-run")
    assert _ledger_rows(memory_store_sql) == before
    actual, actual_out, _ = invoke("import", "--from", str(source))
    after = _ledger_rows(memory_store_sql)
    assert actual == expected_code
    assert preview == actual, preview_out + actual_out
    count = 1 if stored else 2
    written = len(after[0]) - len(before[0])
    assert written == (0 if expected_code else count)
    summary = f"Ledger: {count} records read, {written} written, 0 already present, errors: {expected_code}"
    assert summary in preview_out and summary in actual_out
    if expected_code:
        assert after == before


def test_import_rejects_malformed_preferences_json(sandbox, tmp_path, moved_lcm, memory_store_sql):
    source = _source(tmp_path / "trial")
    (source / "heart/preferences.json").write_text('{"version": 1, "records": [', encoding="utf-8")

    code, output, error = invoke("import", "--from", str(source))

    _assert_empty_ledger(memory_store_sql)
    assert code == 1, output + error
    assert "preferences.json" in output + error


@pytest.mark.parametrize("damage", ["broken journal line", "unknown actor", "missing journal line"])
def test_each_error_prevents_the_entire_ledger_import(sandbox, tmp_path, moved_lcm, memory_store_sql, damage):
    record = _reviewed_temperament()
    if damage == "unknown actor":
        record["history"][0]["by"] = "unknown"
    if damage == "missing journal line":
        record["evidence"][0]["line"] = 3
    source = _source(tmp_path / "trial", records=[record])
    if damage == "broken journal line":
        with (source / "heart/journal.jsonl").open("a", encoding="utf-8") as stream:
            stream.write("{\n")

    code, output, error = invoke("import", "--from", str(source))

    assert code == 1, output + error
    assert "heart/preferences.json" in output
    _assert_empty_ledger(memory_store_sql)
    assert len(heart_rows(memory_store_sql)) == 2


def test_import_reports_multiple_ledger_errors_without_partial_taste_writes(
    sandbox, tmp_path, moved_lcm, memory_store_sql,
):
    valid = _reviewed_temperament()
    bad_strength = {**_temperament("Folding sample sheets"), "id": "p002", "strength": 2}
    bad_history = {**_temperament("Counting sample beads"), "id": "p003"}
    bad_history["history"][0]["by"] = "unrecognized_actor"
    bad_evidence = {**_reviewed_temperament(), "id": "p004", "label": "Arranging sample chips"}
    bad_evidence["norm"] = bad_evidence["label"].lower()
    bad_evidence["evidence"][0]["line"] = 99
    source = _source(tmp_path / "trial", records=[valid, bad_strength, bad_history, bad_evidence])

    code, output, error = invoke("import", "--from", str(source))

    _assert_empty_ledger(memory_store_sql)
    assert code == 1, output + error
    diagnostics = (output + error).lower()
    assert "strength" in diagnostics
    assert "unrecognized_actor" in diagnostics or "history" in diagnostics and "by" in diagnostics
    assert "evidence" in diagnostics and "99" in diagnostics
