"""``lina work run``: a record change and its hand-off to ``lina worker`` are one transaction, and one run may cover
several to-dos without closing any of them (lina#36).

The worker is the real hub with no work device configured, so a handed-out task is queued: its job file under
``$LINA_HOME/worker/hub/jobs`` is the observable "the worker has it"."""

import json

import pytest

from hermes_cli.work import ledger
from hermes_cli.work import run as runs
from tests.hermes_cli._work_env import WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.brief = tmp_path / "brief.md"
    env.brief.write_text("Check both machines and write out/report.txt\n", encoding="utf-8")
    env.goal_and_todos(todos=2)
    return env


def _rows(work, sql, params=()):
    db = ledger.open_db()
    try:
        return db.execute(sql, params).fetchall()
    finally:
        db.close()


def _jobs(work) -> dict:
    folder = work.root / "worker" / "hub" / "jobs"
    return {p.stem: json.loads(p.read_text(encoding="utf-8")) for p in folder.glob("*.json")} if folder.is_dir() else {}


def _crash(*_args, **_kwargs):
    raise RuntimeError("the process died before the worker call")


def test_crash_between_record_and_worker_leaves_an_outbox_row_the_next_call_resumes(work, monkeypatch):
    real = runs._call_worker
    monkeypatch.setattr(runs, "_call_worker", _crash)
    with pytest.raises(RuntimeError):
        work.run("run", "submit", "T1", "T2", "--brief", str(work.brief))
    (run_id,) = [r[0] for r in _rows(work, "SELECT id FROM run")]
    assert sorted(r[0] for r in _rows(work, "SELECT todo_id FROM run_todo WHERE run_id = ?", (run_id,))) == ["T1", "T2"]
    assert _rows(work, "SELECT op FROM outbox WHERE done_at IS NULL") == [("submit",)]
    assert _jobs(work) == {}

    monkeypatch.setattr(runs, "_call_worker", real)
    work.ok("list")
    jobs = _jobs(work)
    assert list(jobs) == [run_id]
    assert jobs[run_id]["todo"] == "T1"
    assert _rows(work, "SELECT count(*) FROM outbox WHERE done_at IS NULL") == [(0,)]
    work.ok("list")
    assert list(_jobs(work)) == [run_id]


def test_a_failed_record_write_hands_nothing_to_the_worker(work, monkeypatch):
    from hermes_cli.work import store

    def broken(*_args, **_kwargs):
        raise RuntimeError("the store write failed")
    monkeypatch.setattr(store, "run_event", broken)
    with pytest.raises(RuntimeError):
        work.run("run", "submit", "T1", "--brief", str(work.brief))
    assert _rows(work, "SELECT count(*) FROM run") == [(0,)]
    assert _rows(work, "SELECT count(*) FROM outbox") == [(0,)]
    t1 = next(i for i in work.json("--all")["items"] if i["id"] == "T1")
    assert not any(link.startswith("omo:") for link in t1["links"])
    assert _jobs(work) == {}


def test_one_run_covers_two_todos_and_closes_neither_when_it_ends(work):
    work.ok("run", "submit", "T1", "T2", "--brief", str(work.brief))
    (run_id,) = list(_jobs(work))
    job_file = work.root / "worker" / "hub" / "jobs" / f"{run_id}.json"
    job = json.loads(job_file.read_text(encoding="utf-8"))
    job.update(state="done", delivery="ok", host="here")
    job_file.write_text(json.dumps(job), encoding="utf-8")
    work.ok("run-sync")
    assert _rows(work, "SELECT status FROM v_run WHERE id = ?", (run_id,)) == [("done",)]
    assert _rows(work, "SELECT id, status, reason FROM v_todo WHERE id IN ('T1', 'T2') ORDER BY id") == [
        ("T1", "waiting", "review"), ("T2", "waiting", "review")]
    items = {i["id"]: i for i in work.json("--all")["items"]}
    assert (items["T1"]["state"], items["T2"]["state"]) == ("open", "open")

    work.ok("edit", "T1", "--check", "true")
    items = {i["id"]: i for i in work.json("--all")["items"]}
    assert (items["T1"]["state"], items["T1"]["done_by"]) == ("done", "검사")
    assert items["T2"]["state"] == "open"
    work.ok("check")
    assert {i["id"]: i["state"] for i in work.json("--all")["items"]}["T2"] == "open"


def test_hand_off_verbs_write_their_event_before_the_worker_call(work, monkeypatch):
    work.ok("run", "submit", "T1", "--brief", str(work.brief))
    (run_id,) = list(_jobs(work))
    real = runs._call_worker
    monkeypatch.setattr(runs, "_call_worker", _crash)
    with pytest.raises(RuntimeError):
        work.run("run", "cancel", run_id, "--why", "the plan changed")
    assert _rows(work, "SELECT kind FROM event WHERE run_id = ? ORDER BY id DESC LIMIT 1", (run_id,)) == [
        ("run_cancelled",)]
    assert _rows(work, "SELECT op FROM outbox WHERE done_at IS NULL") == [("cancel",)]
    monkeypatch.setattr(runs, "_call_worker", real)
    work.ok("list")
    assert _jobs(work)[run_id].get("cancel_why") == "the plan changed"
    assert _rows(work, "SELECT status FROM v_run WHERE id = ?", (run_id,)) == [("dropped",)]


def test_a_work_card_hands_out_every_todo_linked_to_it(work):
    work.ok("card", "link", "t_0123abcd", "--goal", "G1")
    work.ok("card", "link", "t_0123abcd", "--todo", "T1", "T2")
    assert "T1" in work.ok("card", "show", "t_0123abcd") and "G1" in work.ok("card", "show", "t_0123abcd")
    work.ok("run", "submit", "--card", "t_0123abcd", "--brief", str(work.brief))
    (run_id,) = list(_jobs(work))
    assert sorted(r[0] for r in _rows(work, "SELECT todo_id FROM run_todo WHERE run_id = ?", (run_id,))) == ["T1", "T2"]
    assert _rows(work, "SELECT kind, target FROM link WHERE goal_id = 'G1'") == [("kanban", "t_0123abcd")]
