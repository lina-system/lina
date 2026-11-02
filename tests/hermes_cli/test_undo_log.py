"""Reference journal validation, filtering and non-executing outside steps."""

import argparse
import json
from concurrent.futures import ThreadPoolExecutor

import pytest

pytest_plugins = ["tests.hermes_cli.test_undo_support"]

OUTSIDE = {"type": "outside", "service": "notes", "id": "page-1"}


def test_log_show_and_last_filter_metadata(undo):
    # Given a recorded outside reference.
    assert undo.log([OUTSIDE])[0] == 0
    # When viewing it as its bot.
    row = json.loads(undo.ok("show", "change-1"))
    output = undo.ok("last", "--bot", "alpha")
    # Then only references are returned and selection is bot/time scoped.
    assert row["objects"][0]["id"] == "page-1"
    assert "change-1" in output
    assert "change-1" not in undo.ok("last", "--bot", "beta")
    undo.clock[0] += 86401
    assert "change-1" not in undo.ok("last", "--bot", "alpha")


@pytest.mark.parametrize("field", ["body", "content", "before"])
def test_reference_rejects_content_fields(undo, field):
    # Given an object carrying body-like data; when logging it.
    code, _, _ = undo.log([dict(OUTSIDE, **{field: "private body"})])
    # Then no entry is accepted.
    assert code == 2 and undo.journal() == []


@pytest.mark.parametrize("kwargs", [{"what": "line\nbreak"}, {"until": "tomorrow"}, {"kind": "invalid"}])
def test_invalid_entry_metadata_refused(undo, kwargs):
    # Given invalid metadata; when logging it.
    code, _, _ = undo.log([OUTSIDE], **kwargs)
    # Then no journal entry is accepted.
    assert code == 2 and undo.journal() == []


def test_duplicate_change_id_refused(undo):
    # Given an existing identity.
    assert undo.log([OUTSIDE])[0] == 0
    # When logging that identity again.
    code, _, _ = undo.log([OUTSIDE])
    # Then the first entry is retained alone.
    assert code == 2 and len(undo.journal()) == 1


def test_incomplete_journal_prevents_append(undo):
    # Given an interrupted append.
    assert undo.log([OUTSIDE])[0] == 0
    path = undo.undo_root / "journal.jsonl"
    with path.open("a") as stream:
        stream.write('{"incomplete":')
    before = path.read_bytes()
    # When writing another entry.
    code, _, _ = undo.log([OUTSIDE], change_id="second")
    # Then the broken tail is not silently extended.
    assert code == 2 and path.read_bytes() == before


@pytest.mark.parametrize("kind", ["revert", "compensate", "none"])
def test_outside_step_never_executes(undo, monkeypatch, kind):
    import subprocess
    # Given a reference-only external step and no subprocess permission.
    def forbidden(*args, **kwargs):
        pytest.fail("outside step executed a command")
    monkeypatch.setattr(subprocess, "run", forbidden)
    assert undo.log([OUTSIDE], kind=kind, what="secret=synthetic-only")[0] == 0
    # When applying it.
    undo.ok("apply", "change-1")
    # Then only the masked journal remains and no inverse claims an outside effect.
    assert "synthetic-only" not in json.dumps(undo.journal())
    assert len(undo.journal()) == 1


def test_expired_revert_refused(undo):
    # Given an expired outside revert window.
    assert undo.log([OUTSIDE], until="2020-01-01T00:00:00Z")[0] == 0
    # When applying it.
    code, _, _ = undo.run("apply", "change-1")
    # Then no undo is claimed.
    assert code == 2 and len(undo.journal()) == 1


def test_concurrent_journal_appends_preserve_all_entries(undo):
    from hermes_cli.undo import journal
    from hermes_cli.undo.models import Entry, OutsideRef
    from hermes_cli.undo.storage import Store, timestamp
    # Given independent cooperating writers.
    def append(index):
        return journal.append(Store(), Entry(at=timestamp(), bot="alpha", change_id=f"parallel-{index}",
                                             what="Concurrent fixture", how="No undo", kind="none",
                                             objects=[OutsideRef(**OUTSIDE)]))
    # When appending concurrently.
    with ThreadPoolExecutor(max_workers=4) as pool:
        list(pool.map(append, range(8)))
    # Then no entry is lost or interleaved.
    assert len({r["change_id"] for r in undo.journal()}) == 8


def test_parser_builder_dispatches_without_root_registration(undo):
    from hermes_cli.subcommands.undo import build_undo_parser
    # Given an independent parent parser.
    parser = argparse.ArgumentParser()
    build_undo_parser(parser.add_subparsers())
    # When parsing an undo verb.
    args = parser.parse_args(["undo", "last", "--bot", "alpha"])
    # Then the lead can dispatch it through func.
    with pytest.raises(SystemExit) as exit_info:
        args.func(args)
    assert exit_info.value.code == 0
