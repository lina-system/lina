"""``lina work check`` reads a worker run as the worker's job file says, and writes nothing to do so.

A run the worker ended stays ``planned`` in the store until ``lina work run-sync`` runs, and ``planned`` counts as live;
the check reads the run's job file, so the to-do follows its ended run (lina#605). ``check --dry`` leaves the store as
it was, and no check adds a run row for a job file the record does not hold (an imported record keeps its row counts,
which ``lina work import`` compares)."""

import json
from contextlib import closing

import pytest

from hermes_cli.work import ledger, store
from tests.hermes_cli._work_env import WorkEnv
from tests.hermes_cli.test_work_card_state import card_store, computed

RUN = "OT-ALP1101000000"


@pytest.fixture
def work(tmp_path, monkeypatch):
    return WorkEnv(tmp_path, monkeypatch)


def todo_with_run(work: WorkEnv) -> str:
    """A goal and one to-do under it, worked by the worker run RUN (its row in the store: planned)."""
    work.ok("add", "목표", "--by", "alpha", "--area", "Dev", "--title", "빌드 기기 정리", "--basis", "기기 정리해 줘",
            "--done", "빌드 기기가 모두 가동된다", "--check", "false")
    return work.ok("add", "할 일", "--by", "alpha", "--goal", "G1", "--title", "기기 점검", "--why", "작업이 멈추지 않게",
                   "--done", "기기가 답한다", "--check", "false", "--omo", RUN).split()[0]


def job_file(work: WorkEnv, run_id: str, todo: str, **fields) -> None:
    """The worker's job file of *run_id*, as ``lina worker`` leaves it under ``$LINA_HOME/worker/hub/jobs``."""
    folder = work.root / "worker" / "hub" / "jobs"
    folder.mkdir(parents=True, exist_ok=True)
    job = {"id": run_id, "bot": "alpha", "topic": todo, "todos": [todo], "created": work.clock[0] - 600,
           "started": work.clock[0] - 500, **fields}
    (folder / f"{run_id}.json").write_text(json.dumps(job), encoding="utf-8")


def run_status(run_id: str):
    with closing(ledger.open_db()) as db:
        return db.execute("SELECT status FROM v_run WHERE id = ?", (run_id,)).fetchone()


def snapshot(work: WorkEnv) -> tuple[dict, dict, dict]:
    """Every table's rows and counts, and the bytes of the store's files."""
    with closing(ledger.open_db()) as db:
        rows, counts = store.dump(db), store.counts(db)
    files = {p.name: p.read_bytes() for p in sorted(work.work.glob("work.db*"))}
    return rows, counts, files


@pytest.mark.parametrize("job,card,status,line", [
    ({"state": "failed", "ended": 1800000000 - 60}, None, "stalled", "막힌 작업"),
    ({"state": "done", "ended": 1800000000 - 60, "delivery": "ok", "host": "here"}, None, "check_left", ""),
    ({"state": "failed", "ended": 1800000000 - 60}, "blocked", "waiting", "카드 기다림"),
], ids=["stopped", "check-left", "card-wait"])
def test_a_run_the_worker_ended_is_read_ended_while_its_row_is_still_planned(work, job, card, status, line):
    # Given a to-do whose run ended in the worker's job file, its store row still planned (no run-sync)
    todo = todo_with_run(work)
    if card:
        card_store(work, {"t_11111111": (card, None)})
        work.ok("link", todo, "kanban:t_11111111")
    job_file(work, RUN, todo, **job)
    assert run_status(RUN) == ("planned",)
    # When
    work.ok("check", "--quiet")
    # Then the to-do follows its ended run, not "working", and the check wrote nothing to the run row
    row = computed(work, todo)
    assert (row["status"], line in row["line"]) == (status, True)
    assert run_status(RUN) == ("planned",)


def test_check_dry_reads_the_worker_state_and_leaves_the_store_byte_identical(work):
    # Given an ended run whose row is still planned, and a job file the record holds no run row for
    todo = todo_with_run(work)
    job_file(work, RUN, todo, state="failed", ended=work.clock[0] - 60)
    job_file(work, "OT-ALP1101000001", todo, state="done", ended=work.clock[0] - 60)
    before = snapshot(work)
    # When
    out = work.ok("check", "--dry")
    # Then the dry check reports the ended run, and every table, every count and the store's files are unchanged
    assert "막힌 작업" in out
    after = snapshot(work)
    assert after[1] == before[1]
    assert after[0] == before[0]
    assert after[2] == before[2]


def test_check_adds_no_run_row_a_job_file_names_but_the_record_does_not_hold(work):
    # Given an ended run whose row is still planned, and a job file the record holds no run row for
    todo = todo_with_run(work)
    job_file(work, RUN, todo, state="failed", ended=work.clock[0] - 60)
    job_file(work, "OT-ALP1101000001", todo, state="done", ended=work.clock[0] - 60)
    rows, counts, _ = snapshot(work)
    # When
    work.ok("check")
    # Then the run tables hold the same rows as before
    after_rows, after_counts, _ = snapshot(work)
    assert (after_rows["run"], after_rows["run_todo"]) == (rows["run"], rows["run_todo"])
    assert (after_counts["run"], after_counts["run_todo"]) == (counts["run"], counts["run_todo"])
    assert computed(work, todo)["status"] == "stalled"
