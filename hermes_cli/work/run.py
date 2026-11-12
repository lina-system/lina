"""``lina work run``: hand a to-do (or every to-do of a work card) to ``lina worker`` and follow the run.

A record change and its hand-off are one transaction: ``submit`` writes, in one store transaction, the covered
to-dos' new record lines (an ``omo:<run id>`` link each, which projects the ``run`` and ``run_todo`` rows) and an
``outbox`` row; only then is the worker called, with the run id reserved in that transaction, and the outbox row
marked done. A crash in between leaves the outbox row pending, and the next ``lina work`` call resumes it (the worker
call is skipped when the worker already holds the job). Steer, answer, accept, review, retry and cancel go the same
way: their run event and outbox row first, then the worker. A run's end updates the run row only; a to-do closes
only on its own done-condition check (``state.recompute``/``work check``), never because a run ended.
"""

from __future__ import annotations

import json
from pathlib import Path

from hermes_cli.work import ledger, store
from hermes_cli.work.common import Lock, die, now, warn
from hermes_cli.work.items import get
from hermes_cli.work.kinds import TODO, kind_now
from hermes_cli.work.links import kanban_parts, norm_link
from hermes_cli.work.locale import t
from hermes_cli.work.people import agent, owner

WORKER_ENDED = ("done", "done_empty", "review", "stalled", "failed", "cancelled")
VERDICT_OPS = {"accept": True, "review": False}


class HandOffError(RuntimeError):
    """The worker refused the hand-off for good (bad brief, unknown runner): the run is recorded as failed."""


class HandOffNotYet(RuntimeError):
    """The worker could not carry the hand-off out now (a cancel no device acknowledged): the outbox row stays
    pending and the next ``lina work`` call tries again. Nothing is recorded as done."""


# ---------------------------------------------------------------- the worker seam
def _worker_root() -> Path:
    from hermes_cli.worker import settings
    return settings.root(ledger.current().base)


def hub(reserved: str | None = None):
    """A ``lina worker`` hub; *reserved* makes its next new task id the run id this record already holds."""
    from hermes_cli.worker.hub import Hub
    from hermes_cli.worker.hub_store import Store

    class Reserved(Store):
        def new_id(self, agent_id: str, prefix: str = "OT") -> str:
            if prefix == "OT" and reserved:
                return reserved
            return super().new_id(agent_id, prefix)

    lines: list[str] = []
    h = Hub(_worker_root(), out=lines.append)
    h.store = Reserved(_worker_root())
    h.lines = lines
    return h


def reserve_id(who: str) -> str:
    from hermes_cli.worker.hub_store import Store
    base = Store(_worker_root()).new_id(who)
    candidate, n = base, 1
    while store.exists(ledger.db(), "run", candidate):
        n += 1
        candidate = f"{base}-{n}"
    return candidate


def worker_jobs(folder=None) -> list[dict]:
    """The worker's job files (``$LINA_HOME/worker/hub/jobs``, or *folder*) in the shape ``store.sync_runs`` reads."""
    from hermes_cli.worker.hub_store import Store
    if folder:
        p = Path(folder).expanduser()
        files = sorted(p.glob("*.json")) if p.is_dir() else [p]
        jobs = [json.loads(f.read_text(encoding="utf-8-sig")) for f in files if f.is_file()]
    else:
        jobs = Store(_worker_root()).all()
    return [_job_row(j) for j in jobs if isinstance(j, dict) and j.get("id")]


def _job_row(job: dict) -> dict:
    row = {k: job.get(k) for k in ("id", "bot", "topic", "mode", "runner", "created", "started")}
    row["todos"] = list(job.get("todos") or [])
    if job.get("todo"):
        row["todo"] = job["todo"]
    row["device"] = job.get("host")
    state = job.get("state")
    if state in WORKER_ENDED:
        row["ended"] = job.get("ended") or job.get("ended_at") or now()
        row["rc"] = 0 if state == "done" else 1
        if job.get("delivery") == "ok":
            row["result"] = job.get("out_dir") or str(_worker_root() / "hub" / "out" / job["bot"] / job["id"])
    if state == "cancelled":
        row["cancelled"] = row["ended"]
    return row


# ---------------------------------------------------------------- outbox
def _outbox(db, run_id: str, op: str, payload: dict, who: str) -> int:
    store.actor(db, who)
    return db.execute("INSERT INTO outbox (run_id, op, payload, created_at, attempts, agent_id) "
                      "VALUES (?, ?, ?, ?, ?, ?) RETURNING id",
                      (run_id, op, json.dumps(payload, ensure_ascii=False), store.utc(now()), 0,
                       who)).fetchone()[0]


def pending() -> list[tuple]:
    return ledger.db().execute(f"SELECT id, run_id, op, {ledger.db().jtext('payload')} FROM outbox "
                               "WHERE done_at IS NULL ORDER BY id").fetchall()


def _place_saved(h, job: dict) -> None:
    """A job the worker saved (``new``) but never placed because the process died: start or queue it now, as
    ``Hub.submit`` would have, so ``tick`` picks it up instead of leaving it ``new`` for good."""
    from hermes_cli.worker.hub import HubError
    from hermes_cli.worker.runners.base import RunnerError
    from hermes_cli.worker.transport import TransportError
    try:
        h.place(job)
    except (TransportError, HubError, RunnerError, OSError) as exc:
        if (h.store.load(job["id"]) or {}).get("state") == "placing":
            # The placement is recorded and may already be on the device: the tick resumes it there, as
            # ``Hub._submit_one`` leaves it; re-queuing would let a second bid start the task twice.
            h.store.receipt(job["id"], "publish_failed_resumes", error=str(exc)[:300])
            return
        job.update(h.store.update(job["id"], state="queued") or {})
        h.store.receipt(job["id"], "start_failed_requeued", error=str(exc)[:300])


def _submit(h, run_id: str, payload: dict) -> None:
    job = h.store.load(run_id)
    if job:
        if job.get("state") == "new":
            _place_saved(h, job)
        elif job.get("state") == "placing":
            # The worker recorded its device and may have published the request before the crash: finish that
            # placement on the same device instead of bidding again (a second bid could start the task twice).
            h.resume_placement(job["id"])
        return  # the worker took it before the crash: nothing to hand out again
    opts = dict(payload["opts"], request_key=payload["request_key"])
    if opts.get("card"):
        opts["card"] = dict(opts["card"])
    code = h.submit(opts)
    if code or not h.store.load(run_id):
        raise HandOffError(" | ".join(h.lines)[-300:] or t("작업을 넘기지 못했다"))


def _send(h, run_id: str, payload: dict) -> None:
    h.send(run_id, payload["message"], request_key=payload["request_key"])


def _verdict(h, run_id: str, payload: dict) -> None:
    h.verdict(run_id, VERDICT_OPS[payload["op"]], payload.get("note") or "")


def _cancel(h, run_id: str, payload: dict) -> None:
    h.cancel(run_id, payload.get("why") or "")


CALLS = {"submit": _submit, "send": _send, "retry": _send, "accept": _verdict, "review": _verdict, "cancel": _cancel}


def _call_worker(oid: int, op: str, run_id: str, payload: dict) -> None:
    from hermes_cli.worker.hub import HubError
    from hermes_cli.worker.runners.base import RunnerError
    h = hub(run_id if op == "submit" else None)
    try:
        # The outbox row is the request: resuming it after a lost reply is the same request, never a second one.
        CALLS[op](h, run_id, dict(payload, op=op, request_key=f"lina-work:{oid}"))
    except HubError as exc:
        # A cancel the device did not acknowledge leaves the run going there: never record it cancelled.
        if op == "cancel" and h.store.load(run_id):
            raise HandOffNotYet(str(exc)) from exc
        raise HandOffError(str(exc)) from exc
    except RunnerError as exc:
        raise HandOffError(str(exc)) from exc
    for line in h.lines:
        print(line)


def _finish(oid: int, run_id: str, op: str, error: str | None) -> None:
    db = ledger.db()
    with store.transaction(db):
        db.execute("UPDATE outbox SET done_at = ?, attempts = attempts + 1, last_error = ? WHERE id = ?",
                   (store.utc(now()), error, oid))
        if error and op == "submit":
            db.execute("UPDATE run SET ended_at = ?, exit_code = ? WHERE id = ? AND ended_at IS NULL",
                       (store.utc(now()), 1, run_id))
            store.run_event(db, run_id, "run_ended", "lina-work", {"op": op, "error": error}, error,
                            store.utc(now()))
        elif op == "cancel":
            db.execute("UPDATE run SET cancelled_at = ? WHERE id = ?", (store.utc(now()), run_id))
        _sync_one(db, run_id)


def _retry_later(oid: int, error: str) -> None:
    db = ledger.db()
    with store.transaction(db):
        db.execute("UPDATE outbox SET attempts = attempts + 1, last_error = ? WHERE id = ?", (error[:500], oid))


def dispatch() -> int:
    """Hand every pending outbox row to the worker, oldest first. -> rows done."""
    from hermes_cli.worker.transport import TransportError
    done = 0
    for oid, run_id, op, payload in pending():
        try:
            _call_worker(oid, op, run_id, json.loads(payload))
        except HandOffError as exc:
            _finish(oid, run_id, op, str(exc)[:500])
            warn(t("{run}: 작업이 받지 않았다 — {why}", run=run_id, why=exc))
            continue
        except (HandOffNotYet, TransportError, OSError) as exc:
            _retry_later(oid, str(exc))
            warn(t("{run}: 작업에 아직 못 넘김, 다음 호출 때 다시 — {why}", run=run_id, why=exc))
            break
        _finish(oid, run_id, op, None)
        done += 1
    return done


def resume() -> None:
    """Called at the start of every ``lina work`` command: finish hand-offs a crash left in the outbox."""
    if not pending():
        return
    with Lock():
        n = dispatch()
    if n:
        warn(t("끊겼던 작업 넘기기 {n}건을 마저 했다", n=n))


# ---------------------------------------------------------------- record side
def worker_job(run_id: str) -> dict | None:
    """*run_id*'s worker job file in the shape ``store.sync_runs`` reads, or None when the worker has no such job."""
    from hermes_cli.worker.hub_store import Store
    job = Store(_worker_root()).load(run_id)
    return _job_row(job) if job else None


def _sync_one(db, run_id: str) -> None:
    row = worker_job(run_id)
    if row:
        store.sync_runs(db, [row], [], store.utc(now()))


def card_todos(card: str) -> list[str]:
    """The ledger to-dos a work card covers: every open to-do linked ``kanban:<card>``."""
    link = norm_link("kanban:" + card if not card.startswith("kanban:") else card, owner())
    recs, order = ledger.read_all()
    return [i for i in order if kind_now(recs[i]["kind"]) == TODO and recs[i].get("state") == "open"
            and link in (recs[i].get("links") or [])]


def _covered(a, recs: dict) -> list[dict]:
    ids = [i.upper() for i in a.todos or []]
    if a.card:
        ids += [i for i in card_todos(a.card) if i not in ids]
    if not ids:
        die(t("넘길 할 일이 없다: 할 일 id나 --card <작업 카드>(그 카드에 붙은 할 일)를 준다"))
    out = []
    for i in dict.fromkeys(ids):
        r = get(recs, i)
        if kind_now(r["kind"]) != TODO or r.get("state") != "open":
            die(t("{id}는 열린 할 일이 아니다", id=i))
        out.append(r)
    return out


def _opts(a, todos: list[dict], who: str) -> dict:
    opts = {"briefs": [str(Path(a.brief).expanduser().resolve())], "mode": a.mode, "repo": a.repo,
            "inputs": [str(Path(x).expanduser().resolve()) for x in a.input or []], "todo": todos[0]["id"],
            "topic": "+".join(r["id"] for r in todos), "host": a.host, "model": a.model, "note": a.note,
            "runner": a.runner, "bot": who, "wait": None}
    if a.card:
        board, card = kanban_parts(norm_link("kanban:" + a.card if not a.card.startswith("kanban:") else a.card,
                                             owner()))
        opts["card"] = {"task": card, "board": board or None}
    return {k: v for k, v in opts.items() if v is not None or k == "wait"}


def cmd_submit(a) -> None:
    if not Path(a.brief).expanduser().is_file():
        die(t("브리프 파일이 없다: {path}", path=a.brief))
    with Lock():
        recs, _ = ledger.read_all()
        todos = _covered(a, recs)
        who = agent(a.as_) if a.as_ else (todos[0]["by"] if todos[0]["by"] != owner() else todos[0].get("from"))
        db = ledger.db()
        with store.transaction(db):
            run_id = reserve_id(who)
            for r in todos:
                links = list(dict.fromkeys((r.get("links") or []) + ["omo:" + run_id]))
                ledger.append_in_txn(dict(r, links=links), "link", who, progress_at=now())
            opts = _opts(a, todos, who)
            db.execute("UPDATE run SET runner = ?, title = ?, mode = ?, topic = ?, agent_id = ?, goal_id = "
                       "coalesce(goal_id, ?) WHERE id = ?",
                       (a.runner or "omo", opts["topic"], a.mode, opts["topic"], store.actor(db, who),
                        todos[0].get("parent") if store.exists(db, "goal", todos[0].get("parent")) else None,
                        run_id))
            store.run_event(db, run_id, "run_started", who, {"op": "submit", "todos": [r["id"] for r in todos]},
                            None, store.utc(now()))
            _outbox(db, run_id, "submit", {"opts": opts}, who)
        print(t("{run}: 할 일 {ids}을 넘긴다", run=run_id, ids=", ".join(r["id"] for r in todos)))
        dispatch()


def _need_run(run_id: str) -> tuple:
    row = ledger.db().execute("SELECT id, agent_id FROM run WHERE id = ?", (run_id,)).fetchone()
    if not row:
        die(t("기록에 없는 작업: {run}", run=run_id))
    return row


def _hand_off(run_id: str, op: str, kind: str, payload: dict, who: str | None) -> None:
    with Lock():
        _, owner_agent = _need_run(run_id)
        who = who or owner_agent
        db = ledger.db()
        with store.transaction(db):
            store.run_event(db, run_id, kind, who, dict(payload, op=op), payload.get("message") or
                            payload.get("note") or payload.get("why"), store.utc(now()))
            _outbox(db, run_id, op, payload, who)
        dispatch()


def cmd_steer(a) -> None:
    _hand_off(a.run, "send", "run_activity", {"message": a.message}, agent(a.as_) if a.as_ else None)


def cmd_answer(a) -> None:
    _hand_off(a.run, "send", "answer", {"message": a.message}, agent(a.as_) if a.as_ else None)


def cmd_accept(a) -> None:
    _hand_off(a.run, "accept", "status", {"note": a.note or ""}, agent(a.as_) if a.as_ else None)


def cmd_review(a) -> None:
    _hand_off(a.run, "review", "status", {"note": a.why}, agent(a.as_) if a.as_ else None)


def cmd_retry(a) -> None:
    message = a.message or t("앞선 실행이 끝났다. 남은 것을 이어서 하고 report.md를 다시 채운다.")
    _hand_off(a.run, "retry", "run_activity", {"message": message}, agent(a.as_) if a.as_ else None)


def cmd_cancel(a) -> None:
    _hand_off(a.run, "cancel", "run_cancelled", {"why": a.why or ""}, agent(a.as_) if a.as_ else None)


def _show(run_id: str) -> None:
    db = ledger.db()
    row = db.execute("SELECT id, runner, status, reason, started_at, ended_at, result_path FROM v_run WHERE id = ?",
                     (run_id,)).fetchone()
    todos = db.execute("SELECT vt.id, vt.status, vt.reason, vt.title FROM run_todo rt JOIN v_todo vt "
                       "ON vt.id = rt.todo_id WHERE rt.run_id = ? ORDER BY vt.id", (run_id,)).fetchall()
    print(t("기록: {run} · {runner} · {status}", run=row[0], runner=row[1], status=row[2])
          + (f" ({row[3]})" if row[3] else "") + (" · " + t("결과 {path}", path=row[6]) if row[6] else ""))
    for tid, code, reason, title in todos:
        print(f"  {tid} {code}" + (f" ({reason})" if reason else "") + f" · {title}")
    left = db.execute("SELECT count(*) FROM outbox WHERE run_id = ? AND done_at IS NULL", (run_id,)).fetchone()[0]
    if left:
        print(t("  넘기지 못한 요청 {n}건 (다음 lina work 호출 때 다시)", n=left))


def _sync(run_id: str) -> None:
    with Lock():
        db = ledger.db()
        with store.transaction(db):
            _sync_one(db, run_id)


def cmd_status(a) -> None:
    from hermes_cli.worker.hub import HubError
    _need_run(a.run)
    h = hub()
    try:
        h.status(a.run, a.json)
    except HubError as exc:
        warn(str(exc))
    for line in h.lines:
        print(line)
    _sync(a.run)
    if not a.json:
        _show(a.run)


def cmd_result(a) -> None:
    from hermes_cli.worker.hub import HubError
    _need_run(a.run)
    h = hub()
    try:
        h.result(a.run)
    except HubError as exc:
        die(str(exc))
    for line in h.lines:
        print(line)
    _sync(a.run)
    with Lock():
        db = ledger.db()
        with store.transaction(db):
            store.run_event(db, a.run, "run_ended", "lina-work", {"op": "result"}, None, store.utc(now()))
    _show(a.run)
