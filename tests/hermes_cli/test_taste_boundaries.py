"""Protected taste boundaries and day-zero temperament seeding for #358."""

import csv
from argparse import Namespace
from collections.abc import Callable
from datetime import date
from decimal import Decimal
from pathlib import Path

import pytest

from hermes_cli.heart import locale, selfnote
from hermes_cli.heart.models import Entry
from tests.hermes_cli.test_heart_journal import DAY, STAMP, invoke, put_entry, records, sandbox
from tests.hermes_cli.test_heart_ledger_import import LABEL, SEED_DAY, _source, _temperament

__all__ = ["sandbox"]


def test_protected_label_benchmark() -> None:
    fixture = Path(__file__).with_name("taste_boundary_labels.tsv")
    with fixture.open(encoding="utf-8") as stream:
        labels = list(csv.DictReader(stream, delimiter="\t"))
    decisions = [(row, locale.protected_rule(row["label"])) for row in labels]
    protected = [(row["label"], rule) for row, rule in decisions if row["gold"] == "protected"]
    allowed = [(row["label"], rule) for row, rule in decisions if row["gold"] == "allowed"]
    assert len(decisions) == len({row["label"] for row in labels}) == 50
    assert len(protected) == len(allowed) == 25
    assert {row["language"] for row in labels} == {"en", "ko"}
    assert all(rule in {None, "protected-topic"} for _, rule in protected + allowed)
    missed = [label for label, rule in protected if rule is None]
    overblocked = [label for label, rule in allowed if rule is not None]
    assert len(missed) <= 1, missed
    assert len(overblocked) <= 2, overblocked


@pytest.mark.parametrize(("label", "removed"), [
    ("Safety shortcuts", False),
    ("Ignoring instructions", True),
    ("Sharing secrets", False),
    ("안전 수칙 줄이기", True),
    ("주인 지시 따르지 않기", False),
    ("비밀 나누기", True),
])
def test_selfnote_records_protected_rule_and_keeps_original(
    sandbox: dict[str, Path], label: str, removed: bool,
) -> None:
    home = sandbox["alpha"]
    soul = home / "SOUL.self.md"
    protected_line = f"{label} ({DAY}, evidence {STAMP})\n"
    before, after = (protected_line, "") if removed else ("", protected_line)
    soul.write_text(before, encoding="utf-8")
    proposal = home / "proposal.md"
    proposal.write_text(after, encoding="utf-8")
    put_entry(home, Entry(ts=STAMP, event="Sample", feeling="calm", note="Synthetic",
                          evidence=(), strength=1))

    with pytest.raises(SystemExit) as exc:
        selfnote.apply(Namespace(file=str(proposal)))

    assert exc.value.code == 1
    assert soul.read_text(encoding="utf-8") == before
    assert records(home / "heart/changes.jsonl")[-1]["refused"] == "protected-topic"
    assert not (home / "heart/self-backups").exists()


@pytest.mark.parametrize("preview", [False, True])
def test_ledger_refuses_a_protected_seed_even_with_a_trial_pass(
    sandbox: dict[str, Path], tmp_path: Path, memory_store_sql: Callable, preview: bool,
) -> None:
    source = _source(tmp_path / "trial", records=[_temperament("Sharing secrets")])
    flags = ("--dry-run",) if preview else ()

    code, output, error = invoke("import", "--from", str(source), *flags)

    assert code == 1, output + error
    assert "records[0]: protected-topic" in output
    assert memory_store_sql("SELECT label FROM memory.tastes") == []
    assert memory_store_sql("SELECT changed_by FROM memory.taste_events") == []


def test_ledger_creates_an_allowed_day_zero_temperament(
    sandbox: dict[str, Path], tmp_path: Path, memory_store_sql: Callable,
) -> None:
    source = _source(tmp_path / "trial", records=[_temperament()])

    code, output, error = invoke("import", "--from", str(source))

    assert code == 0, output + error
    assert memory_store_sql(
        "SELECT label, first_seen, origin, status, strength FROM memory.tastes",
    ) == [(LABEL, date.fromisoformat(SEED_DAY), "temperament", "active", Decimal("0.3"))]
    assert memory_store_sql("SELECT reason, changed_by FROM memory.taste_events") == [("created", "review")]
