"""``lina work json`` / ``list`` on a copied record equal the ledger's output before the move (lina#36).

The synthetic record (tests/hermes_cli/fixtures/work) is imported into the store, then read back through the CLI and
compared with the outputs captured from the ledger on the same record."""

import json

import pytest

from tests.hermes_cli._work_env import FIXTURES, WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.fixture()
    return env


@pytest.mark.parametrize("args,golden", [(["list"], "work-list.txt"), (["list", "--all"], "work-list-all.txt")])
def test_list_matches_the_ledger(work, args, golden):
    assert work.ok(*args) == (FIXTURES / golden).read_text(encoding="utf-8")


def test_json_matches_the_ledger(work):
    actual = work.json("--all")
    for item in actual["items"]:
        item.get("view", {}).pop("activity_at", None)
    assert actual == json.loads((FIXTURES / "work-json.txt").read_text(encoding="utf-8"))


def test_reads_leave_the_record_unchanged(work):
    from hermes_cli.work import ledger, store
    work.ok("json", "--all")
    work.ok("list", "--all")
    work.run("show", "G1")
    db = ledger.open_db()
    try:
        lines = [line for _, line in store.lines(db)]
    finally:
        db.close()
    source = [line.strip() for line in (FIXTURES / "ledger.jsonl").read_text(encoding="utf-8").splitlines()]
    assert lines == source
