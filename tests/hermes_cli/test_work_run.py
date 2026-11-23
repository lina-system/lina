"""``lina work run``: a record change and its hand-off to ``lina worker`` are one transaction, and one run may cover
several to-dos without closing any of them (lina#36).

The worker is the real hub with no work device configured, so a handed-out task is queued: its job file under
``$LINA_HOME/worker/hub/jobs`` is the observable "the worker has it"."""

import json
import sys

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


def test_crash_after_the_worker_saved_a_new_job_places_it_before_the_outbox_is_done(work, monkeypatch):
    from hermes_cli.worker.hub import Hub
    real = Hub.place
    monkeypatch.setattr(Hub, "place", _crash)
    with pytest.raises(RuntimeError):
        work.run("run", "submit", "T1", "--brief", str(work.brief))
    (run_id,) = list(_jobs(work))
    assert _jobs(work)[run_id]["state"] == "new"
    assert _rows(work, "SELECT op FROM outbox WHERE done_at IS NULL") == [("submit",)]

    monkeypatch.setattr(Hub, "place", real)
    work.ok("list")
    assert _jobs(work)[run_id]["state"] == "queued"
    assert _rows(work, "SELECT count(*) FROM outbox WHERE done_at IS NULL") == [(0,)]


def test_crash_after_the_worker_recorded_a_placement_resumes_it_without_bidding_again(work, monkeypatch):
    """The worker saves its device choice (``placing``) before it publishes the request; a crash in between must
    finish that placement on the same device, never bid again (two bids could start the task twice)."""
    from hermes_cli.worker.hub import Hub

    def recorded_then_died(self, job):
        job.update(self.store.update(job["id"], host="here", runs=1, state="placing") or {})
        raise RuntimeError("the process died after recording the placement")
    monkeypatch.setattr(Hub, "place", recorded_then_died)
    with pytest.raises(RuntimeError):
        work.run("run", "submit", "T1", "--brief", str(work.brief))
    (run_id,) = list(_jobs(work))
    assert (_jobs(work)[run_id]["state"], _jobs(work)[run_id]["host"]) == ("placing", "here")

    calls = []
    monkeypatch.setattr(Hub, "place", lambda self, job: calls.append(("place", job["id"])))
    monkeypatch.setattr(Hub, "resume_placement", lambda self, task_id: calls.append(("resume", task_id)) or "here")
    work.ok("list")
    assert calls == [("resume", run_id)]
    assert _rows(work, "SELECT count(*) FROM outbox WHERE done_at IS NULL") == [(0,)]


def test_a_recovered_job_whose_publish_failed_keeps_its_recorded_placement(work, monkeypatch):
    """Recovery of a ``new`` job: when ``place`` records the device and then fails to publish (a lost ssh reply),
    the job stays ``placing`` for the tick to resume on that device; it is never re-queued for a second bid."""
    from hermes_cli.worker.hub import Hub
    from hermes_cli.worker.transport import TransportError
    real = Hub.place
    monkeypatch.setattr(Hub, "place", _crash)
    with pytest.raises(RuntimeError):
        work.run("run", "submit", "T1", "--brief", str(work.brief))
    (run_id,) = list(_jobs(work))
    assert _jobs(work)[run_id]["state"] == "new"

    def recorded_then_lost(self, job):
        job.update(self.store.update(job["id"], host="here", runs=1, state="placing") or {})
        raise TransportError("the ssh reply was lost")
    monkeypatch.setattr(Hub, "place", recorded_then_lost)
    work.ok("list")
    assert (_jobs(work)[run_id]["state"], _jobs(work)[run_id]["host"]) == ("placing", "here")
    assert _rows(work, "SELECT count(*) FROM outbox WHERE done_at IS NULL") == [(0,)]
    monkeypatch.setattr(Hub, "place", real)


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


@pytest.mark.parametrize("in_process", [True, False], ids=["in-process", "subprocess"])
def test_submit_links_todo_without_holding_the_work_lock(work, monkeypatch, in_process):
    import fcntl

    from hermes_cli.worker.hub import Hub

    lock = work.work / ".lock"
    # The submitter already wrote the link. Record the arguments only after the worker's link command succeeds.
    linked = work.tmp / "linked.json"
    if in_process:
        def link(_hub, _job, *args):
            with lock.open() as handle:
                fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
            work.ok(*args)
            linked.write_text(json.dumps(args))

        monkeypatch.setattr(Hub, "_todo", link)
    else:
        command = [sys.executable, "-c",
                   "import fcntl, json, sys; from pathlib import Path; from hermes_cli.work import main\n"
                   "with open(sys.argv[1]) as f: fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)\n"
                   "main(sys.argv[3:])\n"
                   "Path(sys.argv[2]).write_text(json.dumps(sys.argv[3:]))", str(lock), str(linked)]
        work.config(extra=f"worker:\n  todo_link: {{kind: hermes, command: {json.dumps(command)}}}\n")

    code, out, err = work.run("run", "submit", "T1", "--brief", str(work.brief))
    assert code == 0 and linked.exists(), (out, err)
    (run_id,) = _jobs(work)
    assert json.loads(linked.read_text()) == ["link", "T1", f"omo:{run_id}"]
    assert _rows(work, "SELECT attempts, last_error FROM outbox WHERE op = 'submit' AND done_at IS NOT NULL") == [
        (1, None)]


@pytest.mark.parametrize("recovered", [False, True], ids=["in-process", "subprocess-resume"])
def test_accept_links_todo_without_holding_the_work_lock(work, monkeypatch, recovered):
    import fcntl

    from hermes_cli.worker.hub import Hub

    work.ok("run", "submit", "T1", "--brief", str(work.brief))
    (run_id,) = list(_jobs(work))
    accept = ("run", "accept", run_id, "--note", "checked")
    lock = work.work / ".lock"
    if recovered:
        with monkeypatch.context() as interrupted:
            interrupted.setattr(runs, "_call_worker", _crash)
            with pytest.raises(RuntimeError):
                work.run(*accept)
        # Fail immediately on contention, then run the real command against the same record.
        command = [sys.executable, "-c",
                   "import fcntl, sys; from hermes_cli.work import main\n"
                   "with open(sys.argv[1]) as f: fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)\n"
                   "main(sys.argv[2:])", str(lock)]
        work.config(extra=f"worker:\n  todo_link: {{kind: hermes, command: {json.dumps(command)}}}\n")
        work.ok("list")
    else:
        def link(_hub, _job, *args):
            with lock.open() as handle:
                fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
            work.ok(*args)

        monkeypatch.setattr(Hub, "_todo", link)
        work.ok(*accept)

    try:
        todo = ledger.read_all()[0]["T1"]
    finally:
        ledger.close()
    assert [e["ref"] for e in todo.get("evidence", [])] == [f"omo:{run_id}"]
    assert [n["text"] for n in todo.get("notes", [])] == [f"{run_id} checked: checked"]
    assert _jobs(work)[run_id]["verdict"] == "accepted"
    assert _rows(work, "SELECT attempts, last_error FROM outbox WHERE op = 'accept' AND done_at IS NOT NULL") == [
        (1, None)]


@pytest.mark.parametrize(("failed_verb", "reviewed"), [
    ("evidence", False), ("note", False), pytest.param("note", True, id="note-after-review"),
])
def test_accept_todo_write_failure_can_be_retried(work, failed_verb, reviewed):
    work.ok("run", "submit", "T1", "--brief", str(work.brief))
    (run_id,) = _jobs(work)
    accept = ("run", "accept", run_id, "--note", "checked")
    command = [sys.executable, "-c",
               "import sys; from hermes_cli.work import main\n"
               "if sys.argv[1] == sys.argv[2]: sys.exit(23)\n"
               "main(sys.argv[2:])", "none"]
    if reviewed:
        work.config(extra=f"worker:\n  todo_link: {{kind: hermes, command: {json.dumps(command)}}}\n")
        work.ok("run", "accept", run_id, "--note", "first")
        work.ok("run", "review", run_id, "--why", "send back")
    command[-1] = failed_verb
    work.config(extra=f"worker:\n  todo_link: {{kind: hermes, command: {json.dumps(command)}}}\n")

    code, out, err = work.run(*accept)
    assert code != 0, (out, err)
    failed_key = _jobs(work)[run_id].get("work_accept_key")

    command[-1] = "none"  # Restore both verbs against the same to-do.
    work.config(extra=f"worker:\n  todo_link: {{kind: hermes, command: {json.dumps(command)}}}\n")
    work.ok(*accept)
    assert _jobs(work)[run_id].get("work_accept_key")
    work.ok(*accept)
    todo = next(r for r in work.json("--all")["items"] if r["id"] == "T1")
    assert [e["ref"] for e in todo["evidence"]] == [f"omo:{run_id}"]
    expected_notes = ["first", "checked"] if reviewed else ["checked"]
    assert [n["text"] for n in todo["notes"]] == [f"{run_id} checked: {note}" for note in expected_notes]
    assert not failed_key


def _freeze_after_hand_off(work, monkeypatch):
    from hermes_cli.work import common

    call = runs._call_worker
    freeze = work.work / "freeze"

    def finish_then_freeze(*args):
        call(*args)
        freeze.touch()

    monkeypatch.setattr(common, "freeze_wait", lambda: 0)
    monkeypatch.setattr(runs, "_call_worker", finish_then_freeze)
    return freeze


@pytest.mark.parametrize("legacy_worker", [False, True], ids=["current", "before-upgrade"])
def test_accept_retry_after_frozen_reentry_records_evidence_and_note_once(work, monkeypatch, legacy_worker):
    from hermes_cli.worker.hub import Hub

    def link(_hub, _job, *args):
        work.ok(*args)

    monkeypatch.setattr(Hub, "_todo", link)
    work.ok("run", "submit", "T1", "--brief", str(work.brief))
    (run_id,) = _jobs(work)
    accept = ("run", "accept", run_id, "--note", "checked")
    with monkeypatch.context() as interrupted:
        if legacy_worker:
            # A pre-upgrade worker wrote its verdict and both to-do entries, with no completion key.
            interrupted.setitem(runs.CALLS, "accept", lambda h, rid, payload: h.verdict(rid, True, payload["note"]))
        freeze = _freeze_after_hand_off(work, interrupted)
        assert work.run(*accept)[0] == 75
    assert _rows(work, "SELECT op FROM outbox WHERE done_at IS NULL") == [("accept",)]
    freeze.unlink()

    work.ok(*accept)
    try:
        todo = ledger.read_all()[0]["T1"]
    finally:
        ledger.close()
    assert [e["ref"] for e in todo["evidence"]] == [f"omo:{run_id}"]
    assert [n["text"] for n in todo["notes"]] == [f"{run_id} checked: checked"]
    assert _rows(work, "SELECT count(*) FROM outbox WHERE op = 'accept' AND done_at IS NOT NULL") == [(1,)]


def test_submit_retry_after_frozen_reentry_keeps_one_link(work, monkeypatch):
    from hermes_cli.worker.hub import Hub

    monkeypatch.setattr(Hub, "_todo", lambda _hub, _job, *args: work.ok(*args))
    submit = ("run", "submit", "T1", "--brief", str(work.brief))
    with monkeypatch.context() as interrupted:
        freeze = _freeze_after_hand_off(work, interrupted)
        assert work.run(*submit)[0] == 75
    (run_id,) = _jobs(work)
    assert _rows(work, "SELECT op FROM outbox WHERE done_at IS NULL") == [("submit",)]
    freeze.unlink()

    # An explicit submission first resumes the pending run, then reserves its own ID.
    work.ok(*submit)
    todo = next(r for r in work.json("--all")["items"] if r["id"] == "T1")
    (new_run,) = _jobs(work).keys() - {run_id}
    assert [link for link in todo["links"] if link.startswith("omo:")] == [f"omo:{run_id}", f"omo:{new_run}"]
    assert _rows(work, "SELECT count(*) FROM outbox WHERE op = 'submit' AND done_at IS NOT NULL") == [(2,)]


def test_a_cancel_the_device_did_not_acknowledge_stays_pending_and_the_run_is_not_cancelled(work, monkeypatch):
    """An unreachable device answers the cancel with a HubError ("the task is not cancelled, try again"). The run
    keeps going there, so the record must not say cancelled: the outbox row stays pending, ``cancelled_at`` stays
    empty, and the next ``lina work`` call tries the cancel again."""
    from hermes_cli.worker.hub import Hub, HubError
    work.ok("run", "submit", "T1", "--brief", str(work.brief))
    (run_id,) = list(_jobs(work))
    real = Hub.cancel

    def unreachable(self, task_id, why=""):
        raise HubError(f"{task_id}: the cancel did not reach build-a (rc 255: ssh: connect timed out); "
                       "the task is not cancelled, try again")

    monkeypatch.setattr(Hub, "cancel", unreachable)
    work.run("run", "cancel", run_id, "--why", "the plan changed")
    assert _rows(work, "SELECT cancelled_at FROM run WHERE id = ?", (run_id,)) == [(None,)]
    assert _rows(work, "SELECT op FROM outbox WHERE done_at IS NULL") == [("cancel",)]
    assert _rows(work, "SELECT status FROM v_run WHERE id = ?", (run_id,)) != [("dropped",)]

    monkeypatch.setattr(Hub, "cancel", real)
    work.ok("list")
    assert _rows(work, "SELECT count(*) FROM outbox WHERE done_at IS NULL") == [(0,)]
    assert _rows(work, "SELECT cancelled_at IS NOT NULL FROM run WHERE id = ?", (run_id,)) == [(1,)]


def test_a_work_card_hands_out_every_todo_linked_to_it(work):
    work.ok("card", "link", "t_0123abcd", "--goal", "G1")
    work.ok("card", "link", "t_0123abcd", "--todo", "T1", "T2")
    assert "T1" in work.ok("card", "show", "t_0123abcd") and "G1" in work.ok("card", "show", "t_0123abcd")
    work.ok("run", "submit", "--card", "t_0123abcd", "--brief", str(work.brief))
    (run_id,) = list(_jobs(work))
    assert sorted(r[0] for r in _rows(work, "SELECT todo_id FROM run_todo WHERE run_id = ?", (run_id,))) == ["T1", "T2"]
    assert _rows(work, "SELECT kind, target FROM link WHERE goal_id = 'G1'") == [("kanban", "t_0123abcd")]
