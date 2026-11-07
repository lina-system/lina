"""The goal drive (lina#225): rules the hub runs on the runs of a goal's work cards, so routine moves between a run
and the gates cost no model turn in a card's session. ``Hub.tick`` calls :func:`tick` once, after its own loops.

A hub job is driven when its card (``job["card"]``) is a work card of a goal, laid out by ``hermes kanban plan``
(its body opens with ``## Goal`` and ``<goal card> — <title>``). Cards are read with ``kanban show --json`` and
written only through ``kanban block | schedule`` (``kanban_link``'s command and environment); on LINA's own board
through its kanban API. The drive never completes a card: a work card ends through its gates and the judge.

Rules, each at most once per action id ``<goal card>:<work card>:<attempt>:<run>:<rule>``:

* ``red-check``: the run's branch has an open pull request with a red required check (read as ``kanban gate prs``
  reads it): one follow-up naming the check, with the tail of its log.
* ``deliverable``: a run that ended ``done``/``done_empty``/``review`` with no pull request for its branch (a code
  card), no ``report.md`` and no file in ``out/``: one follow-up asking for it.
* ``block``: a follow-up due after ``FOLLOW_UPS`` in one attempt blocks the work card (``needs_input``) with one
  inbox line, and the card is not driven any more.
* ``resubmit``: the run's node is lost (:func:`node_lost`) and, once the device answers again, the run ended or never
  started there: the same brief on another node, from the pushed branch. Never while another run of the same to-do
  is live.
* ``usage-*``: a run stopped by its runner's usage limit waits for the reset (else ``USAGE_WAIT_S``) and is continued
  in the same task, or moves to another node when ``worker.goal_limits.usage_move`` is set. Not a new attempt.

Device writes carry the action id as their request key, so a hub restarted mid-pass repeats nothing; the ledger
``hub/goals/<goal card>.json`` keeps attempts, follow-ups, runs, the start and every action's result. Limits per goal
(``worker.goal_limits``) block the goal card with the limit and the numbers, and the drive stops for that goal.
``worker.goal_drive: false`` switches it off.
"""

from __future__ import annotations

import datetime
import os
import re
import subprocess
import time

from hermes_cli.worker import bidding, kanban_link, node_watch, request_keys, settings
from hermes_cli.worker.hub_store import _replace

LIMITS = {"workers": 3, "attempts": 3, "runs": 40, "wall_h": 24}
FOLLOW_UPS = 3
CHECK_CACHE_S = 60
LOG_LINES = 80
USAGE_WAIT_S = 3600
# The usage-limit wording of OCX, Anthropic and OpenAI runners; a runner's ``usage_limit`` replaces it.
USAGE_LIMIT = (r"(?i)\b429\b|usage[ _-]limit|rate[ _-]limit|too many requests|exceeded your current quota|"
               r"quota exceeded")
DELIVERABLE_STATES = ("done", "done_empty", "review")
USAGE_STATES = ("failed", "stalled", "done_empty")
CLOSED_CARDS = ("done", "archived")
PARKABLE = ("ready", "todo", "scheduled")
_GITHUB_REPO = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
_ISO = r"\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?"
_RESET_AT = re.compile(r"reset\w*[^0-9\n]{0,40}(" + _ISO + ")", re.IGNORECASE)
_RESET_IN = re.compile(r"(?:reset\w*|try again|retry)\s+(?:in|after)\s+(\d+)\s*(seconds?|secs?|s|minutes?|mins?|m|"
                       r"hours?|hrs?|h)\b", re.IGNORECASE)
_UNIT_S = {"s": 1, "m": 60, "h": 3600}


def _errors() -> tuple[type[BaseException], ...]:
    """What one goal's or one run's pass may raise without stopping the others; each is logged."""
    from hermes_cli.kanban_gates import GateError
    from hermes_cli.worker.hub import HubError
    from hermes_cli.worker.runners.base import RunnerError
    from hermes_cli.worker.transport import TransportError
    return (OSError, ValueError, KeyError, subprocess.SubprocessError, kanban_link.CardLinkError, HubError,
            TransportError, RunnerError, GateError)


def enabled(worker: dict) -> bool:
    return worker.get("goal_drive") is not False


def limits(worker: dict) -> dict:
    """``worker.goal_limits`` over :data:`LIMITS`, with ``usage_move`` (default off)."""
    raw = worker.get("goal_limits") if isinstance(worker.get("goal_limits"), dict) else {}
    out = {name: int(raw.get(name) or default) for name, default in LIMITS.items()}
    out["usage_move"] = raw.get("usage_move") is True
    return out


def node_lost(job: dict, store) -> bool:
    """The run's node is unavailable: the run is ``unknown`` because ``node_watch`` sent its node's
    ``host.unavailable`` (``FAILED_CONTACTS`` failed contacts in a row) and that outage names it."""
    return job.get("state") == "unknown" and node_watch.outage_of(store, job) is not None


def goal_of(body: str | None) -> str | None:
    """The goal card a work card belongs to: the first line under its ``## Goal`` heading, ``<goal card> — <title>``
    (``kanban_plan.work_card_body``)."""
    lines = (body or "").splitlines()
    for i, line in enumerate(lines):
        if line.strip() == "## Goal":
            following = next((rest.strip() for rest in lines[i + 1:] if rest.strip()), "")
            head, sep, _ = following.partition(" — ")
            return head.strip() if sep and head.strip() and " " not in head.strip() else None
    return None


def _part(value) -> str:
    return re.sub(r"[^A-Za-z0-9.-]", "-", str(value))


ID_MAX = 128  # request_keys.CALLER_KEY: one leading character and up to 127 more


def action_id(goal: str, card: str, task: str, run: int, rule: str) -> str:
    """``<goal card>:<work card>:<hub task>:<run>:<rule>``, a caller request key (``request_keys.CALLER_KEY``). The hub
    task ID is unique per attempt and per move and survives a hub restart, so it alone names the to-do and the
    attempt. A task ID that would make the id longer than ``ID_MAX`` is written as the first 12 hex digits of its
    sha256."""
    import hashlib
    parts = [_part(p) for p in (goal, card, task, run, rule)]
    if len(":".join(parts)) > ID_MAX:
        parts[2] = hashlib.sha256(str(task).encode("utf-8")).hexdigest()[:12]
    return request_keys.checked(":".join(parts))


def reset_at(text: str, now: float) -> float:
    """When a usage limit named in *text* resets: an ISO time after ``reset``, or ``reset in <n> <unit>``; else
    ``USAGE_WAIT_S`` from *now*."""
    if found := _RESET_AT.search(text):
        try:
            when = datetime.datetime.fromisoformat(found.group(1))
        except ValueError:
            when = None
        if when is not None:
            return (when if when.tzinfo else when.replace(tzinfo=datetime.UTC)).timestamp()
    if found := _RESET_IN.search(text):
        return now + int(found.group(1)) * _UNIT_S[found.group(2)[0].lower()]
    return now + USAGE_WAIT_S


def _iso(at: float) -> str:
    return datetime.datetime.fromtimestamp(at, datetime.UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def _model(profile: dict | None) -> str | None:
    """A recorded model as ``--model`` takes it again."""
    if not profile:
        return None
    if profile.get("alias"):
        return str(profile["alias"])
    text = f"{profile.get('provider')}/{profile.get('id')}"
    return text + (f":{profile['thinking']}" if profile.get("thinking") else "")


class Board:
    """Reads and writes of cards: a Hermes card through ``kanban_link``'s command, a LINA card through its API."""

    def __init__(self, worker: dict):
        self.worker = worker

    def _command(self) -> list[str]:
        return kanban_link.config(self.worker)["command"]

    def show(self, card: dict) -> dict:
        """``{title, status, body}`` of *card*."""
        if kanban_link.on_hermes(card):
            import json
            task = json.loads(kanban_link._kanban(self._command(), card, "show", "--json"))["task"]
            return {key: task.get(key) for key in ("title", "status", "body")}
        from hermes_cli import kanban_db as kb
        from hermes_cli.kanban_db_connect import connect_closing
        with connect_closing(board=card.get("board")) as conn:
            task = kb.get_task(conn, card["task"])
        if task is None:
            raise kanban_link.CardLinkError(f"no card {card['task']}")
        return {"title": task.title, "status": task.status, "body": task.body}

    # The board blocks only a running or ready card and schedules only a todo, ready, running or blocked one: a card
    # in the other states goes through the board's own step to ready first (unblock, or promote for todo).
    _TO_READY = {"block": {"scheduled": "unblock", "todo": "promote"}, "schedule": {"scheduled": "unblock"}}

    def block(self, card: dict, reason: str, status: str | None) -> str | None:
        """Block *card* (``needs_input``); None when the board took it, else why it refused."""
        return self._write(card, "block", status, reason=reason)

    def schedule(self, card: dict, wait_for: str, reason: str, status: str | None) -> str | None:
        """Park *card* on *wait_for*; None when the board took it, else why it refused."""
        return self._write(card, "schedule", status, reason=reason, wait_for=wait_for)

    def _write(self, card: dict, verb: str, status: str | None, **args) -> str | None:
        step = self._TO_READY[verb].get(status or "")
        if kanban_link.on_hermes(card):
            return self._hermes(card, verb, step, **args)
        return self._lina(card, verb, step, **args)

    def _hermes(self, card: dict, verb: str, step: str | None, *, reason: str, wait_for: str | None = None) -> str | None:
        command = self._command()
        tail = ["--kind", "needs_input"] if verb == "block" else ["--wait-for", str(wait_for)]
        try:
            if step:
                kanban_link._kanban(command, card, step, *(["goal drive: to be parked or blocked"]
                                                           if step == "promote" else []))
            kanban_link._kanban(command, card, verb, *tail, "--", reason)
        except kanban_link.CardLinkError as exc:  # a refused write exits non-zero
            return str(exc)[:300]
        return None

    def _lina(self, card: dict, verb: str, step: str | None, *, reason: str, wait_for: str | None = None) -> str | None:
        """Each step is its own board transaction: ``block_task`` and ``schedule_task`` open one that cannot nest."""
        from hermes_cli import kanban_db as kb
        from hermes_cli.kanban_db_connect import connect_closing
        from hermes_cli.kanban_db_wait import parse_wait_for
        with connect_closing(board=card.get("board")) as conn:
            if step == "unblock" and not kb.unblock_task(conn, card["task"], by=kanban_link.AUTHOR,
                                                         reason="goal drive: to be parked or blocked"):
                return "unblock refused"
            if step == "promote":
                promoted, why = kb.promote_task(conn, card["task"], actor=kanban_link.AUTHOR,
                                                reason="goal drive: to be blocked")
                if not promoted:
                    return f"promote refused: {why}"
            if verb == "block":
                done = kb.block_task(conn, card["task"], reason=reason, kind="needs_input")
            else:
                done = kb.schedule_task(conn, card["task"], reason=reason, wait_for=parse_wait_for(str(wait_for)))
            return None if done else f"{verb} refused (card is {kb.get_task(conn, card['task']).status})"


def _settled(job: dict) -> bool:
    """The run ended and its end is on its card: only such a run has a rule to run."""
    from hermes_cli.worker.hub import ENDED
    return job.get("state") in ENDED + ("unknown",) and not job.get("card_pending") and \
        not job.get("card_start_pending")


def _view(hub, board: Board, card: dict) -> dict | None:
    """The card as read at its last run end (``kanban_link.kept_view``), else read once from the board and kept (a
    run made ``unknown`` by an outage has no end on its card)."""
    view = kanban_link.kept_view(hub.store, card)
    if view is None:
        view = board.show(card)
        kanban_link.keep_view(hub.store, card, view)
    return view


def tick(hub) -> None:
    """One pass over every goal whose work cards have a run that ended; a goal or a run that cannot be read is logged
    and left for the next tick. Not in a delegated child's context, where the board refuses writes."""
    if not enabled(hub.worker) or "HERMES_DELEGATED_CHILD_CONTEXT" in os.environ:
        return
    board, errors = Board(hub.worker), _errors()
    by_card: dict[str, list[dict]] = {}
    for job in hub.store.all():
        if job.get("card"):
            by_card.setdefault(kanban_link.card_key(job["card"]), []).append(job)
    goals: dict[str, dict] = {}
    for jobs in by_card.values():
        card = jobs[0]["card"]
        if not any(_settled(job) for job in jobs) and kanban_link.kept_view(hub.store, card) is None:
            continue
        try:
            view = _view(hub, board, card)
        except errors as exc:
            hub.store.log(f"goal drive: card {card['task']} not read: {str(exc)[:200]}")
            continue
        goal = goal_of((view or {}).get("body"))
        if goal is None:
            continue
        entry = goals.setdefault(goal, {"board": card.get("board"), "kind": card.get("kind"), "cards": {}})
        entry["cards"][card["task"]] = {"card": card, "view": view, "jobs": jobs}
    for goal, entry in goals.items():
        with hub.store.lock(f"goal-{_part(goal)}"):
            try:
                Drive(hub, board, goal, entry).run()
            except errors as exc:
                hub.store.log(f"goal drive: goal {goal}: {str(exc)[:300]}")


class Drive:
    """One goal's pass, under the goal's lock."""

    def __init__(self, hub, board: Board, goal: str, entry: dict):
        self.hub, self.board, self.goal, self.entry = hub, board, goal, entry
        self.limits, self.now = limits(hub.worker), time.time()
        self.path = hub.store.root / "goals" / f"{_part(goal)}.json"
        self.ledger = self._load()
        self.goal_card = {"task": goal, "board": entry["board"], **({"kind": entry["kind"]} if entry["kind"] else {})}
        self.statuses: dict[str, str | None] = {}  # this tick's board status of each card acted on
        self.tried: set[tuple[str, str]] = set()  # board writes tried this tick: a refused one waits for the next
        self.halted = False  # a limit was reached this tick, whether or not the board took the goal card's block
        self.created: list[str] = []  # tasks the drive submitted this tick (live, not yet in this pass's jobs)
        self.held: set[str] = set()  # action ids holding a run and a worker until their send is made, this pass only

    def _load(self) -> dict:
        import json
        ledger = json.loads(self.path.read_text(encoding="utf-8-sig")) if self.path.is_file() else {}
        ledger.pop("workers", None)  # an older ledger's held workers: holds now live only inside one pass
        ledger["runs"] = {task: count for task, count in (ledger.get("runs") or {}).items() if ":" not in task}
        for name in ("attempts", "followups", "runs", "actions", "usage", "blocked", "moved", "github", "notes",
                     "outages"):
            ledger.setdefault(name, {})
        ledger.setdefault("goal", self.goal)
        return ledger

    def save(self) -> None:
        _replace(self.path, self.ledger)

    def jobs(self) -> list[dict]:
        return [job for card in self.entry["cards"].values() for job in card["jobs"]]

    # ---------------------------------------------------------------- the pass
    def run(self) -> None:
        if self.ledger.get("stopped") or self.ledger.get("closed"):
            return
        goal_view = self.board.show(self.goal_card)
        self.statuses[self.goal] = goal_view.get("status")
        if goal_view.get("status") in CLOSED_CARDS:
            self.ledger["closed"] = goal_view.get("status")
            self.save()
            return
        self.goal_title = goal_view.get("title") or self.goal
        self._count()
        self.save()
        if self._over_limit():
            return
        errors = _errors()
        for card_id, card in self.entry["cards"].items():
            if card_id in self.ledger["blocked"]:
                continue
            for job in self._latest(card["jobs"]):
                try:
                    self._job(card, job)
                except errors as exc:
                    self.hub.store.log(f"goal drive: {job['id']} on card {card_id}: {str(exc)[:300]}")
                if self.halted or card_id in self.ledger["blocked"]:
                    break
            if self.halted:
                return

    def _count(self) -> None:
        """Attempts by to-do (a run the drive moved keeps its attempt), runs by task and the goal's start."""
        jobs = sorted(self.jobs(), key=lambda job: (job["created"], job["id"]))
        self.ledger.setdefault("started_at", jobs[0]["created"] if jobs else self.now)
        highest: dict[str, int] = {}
        for job in jobs:
            todo = self._todo(job)
            if job["id"] not in self.ledger["attempts"]:
                self.ledger["attempts"][job["id"]] = highest.get(todo, 0) + 1
            highest[todo] = max(highest.get(todo, 0), self.ledger["attempts"][job["id"]])
            if job.get("host"):
                self.ledger["runs"][job["id"]] = int(job.get("runs") or 1)

    def _todo(self, job: dict) -> str:
        return f"{job['card']['task']}/{job.get('todo') or job.get('topic')}"

    def _latest(self, jobs: list[dict]) -> list[dict]:
        """The newest task of each to-do: an older one was moved or replaced."""
        latest: dict[str, dict] = {}
        for job in sorted(jobs, key=lambda job: (job["created"], job["id"])):
            latest[self._todo(job)] = job
        return list(latest.values())

    def _over_limit(self) -> bool:
        from hermes_cli.worker.hub import ENDED
        jobs = self.jobs()
        hours = (self.now - float(self.ledger["started_at"])) / 3600
        per_todo: dict[str, int] = {}
        for job in jobs:
            per_todo[self._todo(job)] = max(per_todo.get(self._todo(job), 0), self.ledger["attempts"][job["id"]])
        found = {
            "wall_h": (hours >= self.limits["wall_h"], f"{hours:.1f} h"),
            "runs": (self.runs_used() > self.limits["runs"], f"{self.runs_used()} runs"),
            "workers": (sum(1 for job in jobs if job.get("state") not in ENDED + ("unknown",)) > self.limits["workers"],
                        f"{sum(1 for job in jobs if job.get('state') not in ENDED + ('unknown',))} live runs"),
            "attempts": (max(per_todo.values(), default=0) > self.limits["attempts"],
                         f"{max(per_todo.values(), default=0)} attempts on one to-do"),
        }
        for name, (hit, value) in found.items():
            if hit:
                self.stop(name, value)
                return True
        return False

    def stop(self, name: str, value: str) -> None:
        """Block the goal card on limit *name* and stop the drive for this goal."""
        reason = (f"{self.goal_title} · 갈림길 · the goal drive stopped at its {name} limit "
                  f"({self.limits[name]}): {value}; raise worker.goal_limits.{name} or end the goal?")
        aid = action_id(self.goal, self.goal, "goal", 0, f"limit-{name}")
        self.halted = True
        if not self.act(aid, lambda _aid: self.block(self.goal_card, reason)):
            return  # the board refused: the goal is not stopped yet, the block is tried again on the next tick
        self.ledger["stopped"] = {"limit": name, "value": value, "reason": reason, "at": self.now}
        self.save()
        self.hub.store.log(f"goal drive: goal {self.goal} stopped: {name} {value}")

    def status(self, card: dict) -> str | None:
        """The card's status on the board now, read once per tick: the kept view is a snapshot of a run's end."""
        if card["task"] not in self.statuses:
            self.statuses[card["task"]] = self.board.show(card).get("status")
        return self.statuses[card["task"]]

    def may_act(self, card: dict) -> bool:
        """Whether the drive may act on the work card: not when it is done, archived or blocked by someone else (the
        ledger notes ``skipped: card <status>``)."""
        status = self.status(card["card"])
        if status in CLOSED_CARDS or status == "blocked":
            if self.ledger["notes"].get(card["card"]["task"]) != f"skipped: card {status}":
                self.ledger["notes"][card["card"]["task"]] = f"skipped: card {status}"
                self.save()
            return False
        return True

    def block(self, card: dict, reason: str) -> str | None:
        """Block *card* for a person; a card already blocked (a block made just before a restart) is left as it is.
        None when the board refused."""
        if self.status(card) == "blocked":
            return "already blocked"
        return reason if self.write(card, "block", reason) else None

    def write(self, card: dict, verb: str, reason: str, wait_for: str | None = None) -> bool:
        """One board write, at most once per card and verb per tick. A refusal is noted (``card_write_refused``) and
        tried again on the next tick."""
        if (verb, card["task"]) in self.tried:
            return False
        self.tried.add((verb, card["task"]))
        status = self.status(card)
        refused = self.board.block(card, reason, status) if verb == "block" else \
            self.board.schedule(card, str(wait_for), reason, status)
        if refused:
            self.ledger["notes"][card["task"]] = f"card_write_refused: {verb} on a {status} card: {refused}"
            self.save()
            self.hub.store.log(f"goal drive: card {card['task']}: {self.ledger['notes'][card['task']]}")
            return False
        self.statuses[card["task"]] = "blocked" if verb == "block" else "scheduled"
        return True

    def act(self, aid: str, do) -> bool:
        """Run *do(aid)* once for action *aid*; its result is kept in the ledger. True when it ran or ran before. A
        *do* that returns None did not happen (the board refused): nothing is kept and it runs again on the next tick.
        A failure is raised, so the action runs again on the next tick (a device write with the same key)."""
        if self.done_before(aid):
            return True
        result = do(aid)
        if result is None:
            return False
        self.ledger["actions"][aid] = {"at": self.now, "result": str(result)[:300]}
        self.save()
        self.hub.store.log(f"goal drive: {aid}: {str(result)[:200]}")
        return True

    def done_before(self, aid: str, recover=None) -> bool:
        """Whether action *aid* already happened: in the ledger, or sent with its request key while the hub stopped
        before the ledger was saved. Then the key record is the receipt: the action is recorded as done from it
        (*recover* gets the record, to fill in what the send's own result would have) and is not sent again."""
        if aid in self.ledger["actions"]:
            return True
        sent = self.hub.store.load_key(aid)
        if not sent or not sent.get("action"):
            return False
        if recover is not None:
            recover(sent)
        self.ledger["actions"][aid] = {"at": self.now, "result": f"recovered from its request key: {sent['action']} "
                                                                f"run {sent.get('run')} of {sent.get('task')}"}
        self.save()
        self.hub.store.log(f"goal drive: {aid}: {self.ledger['actions'][aid]['result']}")
        return True

    # ---------------------------------------------------------------- one run
    def _job(self, card: dict, job: dict) -> None:
        run = int(job.get("runs") or 1)

        def ids(rule: str) -> str:
            return action_id(self.goal, job["card"]["task"], job["id"], run, rule)
        count, by = self.rounds(job)
        if count > FOLLOW_UPS:  # a round the drive did not send went past the limit: it cannot be stopped, only met
            self._block_card(card, job, ids, count, by)
            return
        outage = node_watch.outage_of(self.hub.store, job)
        if outage and self.ledger["outages"].get(job["id"]) != outage:  # an outage of its node not yet settled
            self._node_lost(card, job, ids, outage)
            return
        if self._usage(card, job, ids):
            return
        state = job.get("state")
        if state in DELIVERABLE_STATES and self._github(job) and self._checks(card, job, ids):
            return
        if state in DELIVERABLE_STATES and not job.get("card_pending"):
            self._deliverable(card, job, ids)

    def follow_up(self, card: dict, job: dict, ids, rule: str, message: str) -> None:
        """One follow-up on the run; a follow-up due after ``FOLLOW_UPS`` of this attempt blocks the card instead."""
        from hermes_cli.worker.hub import ENDED
        if self.done_before(ids(rule), lambda _sent: self._counted_follow_up(job)) or not self.may_act(card):
            return
        sent, by = self.rounds(job)
        if sent >= FOLLOW_UPS:
            self._block_card(card, job, ids, sent, by, rule)
            return
        starts = job.get("state") in ENDED  # into an ended run the follow-up starts the next one
        if starts and not self.reserve(ids(rule), job):
            return

        def send(aid: str) -> str:
            self.started(aid, lambda: self.hub.send(job["id"], message, job["card"], request_key=aid))
            self._counted_follow_up(job)
            return f"follow-up round {sent + 1}/{FOLLOW_UPS} ({rule}) on {job['id']}"
        self.act(ids(rule), send)

    def _counted_follow_up(self, job: dict) -> None:
        self.ledger["followups"][job["id"]] = int(self.ledger["followups"].get(job["id"], 0)) + 1

    def reserve(self, aid: str, job: dict) -> bool:
        """Hold one run of the goal's run budget and one live worker for action *aid* until it is sent, so two sends
        in one pass cannot both pass a limit. The holds live only in this pass (``held``), on top of what the hub's
        records say now (:meth:`runs_used`, :meth:`live`): a hub that stops mid-pass leaves nothing held. A spent run
        budget stops the goal; a full worker count defers the send to a later tick (noted). False when it must not be
        sent now."""
        held = len(self.held - {aid})
        used = self.runs_used() + held
        if used + 1 > self.limits["runs"]:
            self.stop("runs", f"the next run would be run {used + 1}")
            return False
        busy = self.live() + held
        if busy + 1 > self.limits["workers"]:
            note = f"deferred: the workers limit ({self.limits['workers']}) is reached ({busy} live)"
            if self.ledger["notes"].get(job["id"]) != note:
                self.ledger["notes"][job["id"]] = note
                self.save()
            return False
        self.held.add(aid)
        return True

    def _tasks(self) -> list[dict]:
        """The goal's tasks as the hub holds them at this moment, with the ones the drive moved in this pass."""
        ids = dict.fromkeys([job["id"] for job in self.jobs()] + self.created)
        return [task for task in map(self.hub.store.load, ids) if task]

    def runs_used(self) -> int:
        """The goal's runs, from the hub's records: each placed task's ``runs``."""
        return sum(int(task.get("runs") or 1) for task in self._tasks() if task.get("host"))

    def live(self) -> int:
        """The goal's live runs, from the hub's records."""
        from hermes_cli.worker.hub import ENDED
        return sum(1 for task in self._tasks() if (task.get("state") or "new") not in ENDED + ("unknown",))

    def started(self, aid: str, send) -> None:
        """Run *send* and give back its hold: once sent, the hub's own records count its run and its task (the
        ledger's ``runs`` is read from them again, as it is at the start of each pass)."""
        try:
            send()
        finally:
            self.held.discard(aid)
        self._recount_runs()

    def _recount_runs(self) -> None:
        self.ledger["runs"] = {task["id"]: int(task.get("runs") or 1) for task in self._tasks() if task.get("host")}

    def rounds(self, job: dict) -> tuple[int, str]:
        """The follow-up rounds of the task (one attempt), whoever sent them, and who sent the last one. A round is a
        run after the first or a follow-up queued into a run that had not started, as the hub's ``follow_up``
        receipts record them; a steer into a live run is not one, nor the drive's own continue after a usage limit."""
        keys = job.get("request_keys") or {}
        resumed = sum(1 for key, sent in keys.items()
                      if key.endswith(":usage-resume") and sent.get("action") in ("requested", "queued"))
        follow_ups = [r for r in self.hub.store.receipts_of(job["id"]) if r["kind"] == "follow_up"]
        count = int(job.get("runs") or 1) - 1 + sum(1 for r in follow_ups if r.get("queued")) - resumed
        return max(0, count), self._sender(follow_ups[-1] if follow_ups else None, keys)

    def _sender(self, receipt: dict | None, keys: dict) -> str:
        if receipt is None:
            return "a send the hub kept no receipt of"
        key = receipt.get("request_key") or next(
            (key for key, sent in keys.items() if str(sent.get("run")) == str(receipt.get("run"))), None)
        return "the goal drive" if key and key.startswith(f"{_part(self.goal)}:") else "the card session"

    def _block_card(self, card: dict, job: dict, ids, count: int, by: str, rule: str | None = None) -> None:
        if ids("block") not in self.ledger["actions"] and not self.may_act(card):
            return
        title = (card["view"] or {}).get("title") or job["card"]["task"]
        reason = (f"{title} · 갈림길 · {count} follow-ups on run {job['id']} did not settle it (the last from {by}"
                  + (f", now: {rule}" if rule else "") + "); continue, rescope or stop?")
        if self.act(ids("block"), lambda _aid: self.block(job["card"], reason)):
            self.ledger["blocked"][job["card"]["task"]] = ids("block")
            self.save()

    def park(self, card: dict, aid: str, wait_for: str, reason: str) -> None:
        """Park the card on *wait_for* once (``kanban schedule --wait-for``); a card in a turn or blocked is left. A
        park the board refuses is noted and tried again on the next tick."""
        if aid in self.ledger["actions"] or not self.may_act(card):
            return

        def schedule(_aid: str) -> str | None:
            status = self.status(card["card"])
            if status not in PARKABLE:
                return f"not parked: card is {status}"
            return f"parked on {wait_for}" if self.write(card["card"], "schedule", reason, wait_for) else None
        self.act(aid, schedule)

    # ---------------------------------------------------------------- rules
    def _github(self, job: dict) -> bool:
        return bool(job.get("repo") and job.get("branch") and _GITHUB_REPO.match(job["repo"]))

    def cached(self, key: str, read):
        """A GitHub read kept ``CHECK_CACHE_S``: at most one per run per tick."""
        kept = self.ledger["github"].get(key)
        if kept and self.now - float(kept["at"]) < CHECK_CACHE_S:
            return kept["value"]
        value = read()
        self.ledger["github"][key] = {"at": self.now, "value": value}
        self.save()
        return value

    def pull_request(self, job: dict) -> dict | None:
        """The newest pull request of the run's branch (any state), or None."""
        from hermes_cli.kanban_gates import gh_json

        def read() -> dict | None:
            found = gh_json("pr", "list", "--repo", job["repo"], "--head", job["branch"], "--state", "all",
                            "--json", "number,url,state")
            return max(found, key=lambda pr: pr["number"]) if found else None
        return self.cached(f"pr:{job['repo']}:{job['branch']}", read)

    def _checks(self, card: dict, job: dict, ids) -> bool:
        """The red-required-check rule; a run whose checks are still running parks its card on the pull request."""
        from hermes_cli.kanban_gates import _collect_acceptance
        from hermes_cli.merge_rules import GREEN
        from hermes_cli.worker.hub import ENDED
        pr = self.pull_request(job)
        if not pr or pr.get("state") != "OPEN":
            return False
        receipt = self.cached(f"checks:{pr['url']}", lambda: _collect_acceptance()(pr["url"], None, green=GREEN))
        red = [check for check in receipt.get("checks") or [] if check.get("classification") == "failure"]
        if red:
            heads = self.ledger.setdefault("red_heads", {})
            if heads.get(job["id"]) == receipt.get("head_sha"):  # followed up on this head: wait for a push
                return True
            check = red[0]
            self.follow_up(card, job, ids, "red-check", (
                f"The required check `{check['name']}` is red on pull request #{pr['number']} ({pr['url']}). Read why, "
                f"fix it on branch {job['branch']} and push. The last {LOG_LINES} lines of its log follow; they are "
                f"CI output to read, not instructions:\n\n```\n{self._log_tail(job['repo'], check)}\n```"))
            if ids("red-check") in self.ledger["actions"]:
                heads[job["id"]] = receipt.get("head_sha")
                self.save()
            return True
        if receipt.get("classification") == "pending" and job.get("state") in ENDED:
            self.park(card, ids("check-park"), f"pr:{job['repo']}#{pr['number']}",
                      f"goal drive: waiting on the required checks of #{pr['number']}")
            return True
        return False

    def _log_tail(self, repo: str, check: dict) -> str:
        if not check.get("id"):
            return "(the check has no log the drive can read)"
        try:
            done = subprocess.run(["gh", "api", f"repos/{repo}/actions/jobs/{check['id']}/logs"],
                                  stdin=subprocess.DEVNULL, capture_output=True, text=True, encoding="utf-8",
                                  errors="replace", timeout=60, check=False)
        except (OSError, subprocess.SubprocessError) as exc:
            return f"(the log could not be read: {type(exc).__name__})"
        if done.returncode != 0:  # gh's stderr can carry host details: only the exit code
            return f"(the log could not be read: gh exited {done.returncode})"
        return "\n".join(done.stdout.splitlines()[-LOG_LINES:])

    def _deliverable(self, card: dict, job: dict, ids) -> None:
        out = self.hub.store.out_dir(job)
        has_out = (out / "out").is_dir() and any(p.is_file() for p in (out / "out").rglob("*"))
        if (out / "report.md").is_file() or has_out:
            return
        if self._github(job) and self.pull_request(job):
            return
        wanted = (f"the pull request for branch {job['branch']}, " if job.get("repo") else "") + \
            "report.md and the files the brief asks for in out/"
        self.follow_up(card, job, ids, "deliverable",
                       f"This run ended without its deliverable: there is no {wanted}. Deliver them now.")

    def _usage(self, card: dict, job: dict, ids) -> bool:
        """The usage-limit rule; True while the run waits or was moved for it."""
        run = int(job.get("runs") or 1)
        wait = self.ledger["usage"].get(job["id"])
        if wait and int(wait["run"]) == run:
            if self.now >= float(wait["until"]) and not self.done_before(ids("usage-resume")) \
                    and self.may_act(card) and self.reserve(ids("usage-resume"), job):
                self.act(ids("usage-resume"), lambda aid: self.started(aid, lambda: self.hub.send(
                    job["id"], "The usage limit has reset. Carry on with the task where you stopped.", job["card"],
                    request_key=aid)) or "continued after the usage limit")
            return True
        if job.get("state") not in USAGE_STATES or not re.search(self._usage_pattern(job), self._end_text(job)):
            return False
        until = reset_at(self._end_text(job), self.now)
        if self.limits["usage_move"] and self.move(card, job, ids, "usage-move", same_attempt=True):
            return True
        self.park(card, ids("usage-park"), f"until:{_iso(until)}",
                  f"goal drive: run {job['id']} waits for its usage limit to reset ({_iso(until)})")
        self.ledger["usage"][job["id"]] = {"run": run, "until": until}
        self.save()
        return True

    def _usage_pattern(self, job: dict) -> str:
        spec = (self.hub.worker.get("runners") or {}).get(job.get("runner")) or job.get("runner_spec") or {}
        return spec.get("usage_limit") or USAGE_LIMIT

    def _end_text(self, job: dict) -> str:
        out = self.hub.store.out_dir(job)
        parts = [job.get("notice") or ""]
        for name in ("stderr.txt", "final.md"):
            if (out / name).is_file():
                parts.append((out / name).read_text(encoding="utf-8-sig", errors="replace")[-4000:])
        return "\n".join(parts)

    def _node_lost(self, card: dict, job: dict, ids, outage: str) -> None:
        """A run an outage of its node made ``unknown`` (``node_watch``). While the outage is open the card is parked;
        once the node answered again, the run is moved only when it did not continue there: it ended (the tick put it
        back under watch and delivered its end) or the device does not have it. The outage is settled for this task
        once the run went on or was moved."""
        from hermes_cli.worker.hub import ENDED, HubError
        from hermes_cli.worker.serve import HUB_TICK_S
        from hermes_cli.worker.transport import TransportError
        if node_lost(job, self.hub.store) and node_watch.outage_open(self.hub.store, job["host"], outage):
            back_by = self.now + node_watch.FAILED_CONTACTS * HUB_TICK_S  # no wait-for names a node's return
            self.park(card, ids("node-park"), f"until:{_iso(back_by)}",
                      f"goal drive: the node of run {job['id']} is unavailable ({outage})")
            return
        state = job.get("state")
        if state == "unknown":  # the node answered, but not for this run
            try:
                remote = self.hub.meta(job)
            except (TransportError, HubError, OSError) as exc:
                remote = {"unreachable": True, "why": str(exc)[:200]}
            if remote.get("unreachable"):
                return
            went_on = not remote.get("missing") and remote.get("state") not in ENDED
        else:
            went_on, remote = state not in ENDED, {"run": job.get("runs"), "state": state}
        if went_on:
            self.act(ids("node-continued"), lambda _aid: f"{job.get('host')} is back: run {remote.get('run')} "
                                                         f"{remote.get('state')}; not moved")
        elif not self.move(card, job, ids, "resubmit", same_attempt=False) or \
                ids("resubmit") not in self.ledger["actions"]:
            return
        self.ledger["outages"][job["id"]] = outage
        self.save()

    def move(self, card: dict, job: dict, ids, rule: str, same_attempt: bool) -> bool:
        """The same brief on another allowed node, from the pushed branch; never while another run of the to-do is
        live. False when it cannot move now (it is tried again on the next tick)."""
        from hermes_cli.worker.hub import ENDED
        attempt = self.ledger["attempts"][job["id"]] + (0 if same_attempt else 1)
        if self.done_before(ids(rule), lambda sent: self._moved(job, sent["task"], attempt)):
            return True
        if not self.may_act(card):
            return True
        live = [other["id"] for other in self.hub.store.all()
                if other.get("card") and other["id"] != job["id"] and self._todo(other) == self._todo(job)
                and other.get("state") not in ENDED + ("unknown",)]
        if live:
            self.hub.store.log(f"goal drive: {job['id']} not moved: {', '.join(live)} of the same to-do is live")
            return False
        if job.get("private_inputs"):
            self.hub.store.log(f"goal drive: {job['id']} not moved: it reads private files on its hub node")
            return False
        if attempt > self.limits["attempts"]:
            self.stop("attempts", f"to-do {self._todo(job)} would start attempt {attempt}")
            return True
        node, why = self._other_node(job)
        if node is None:
            note = f"not moved: no other node can take it now ({why})"
            if self.ledger["notes"].get(job["id"]) != note:
                self.ledger["notes"][job["id"]] = note
                self.save()
            self.hub.store.log(f"goal drive: {job['id']} {note}")
            return False

        if not self.reserve(ids(rule), job):
            return True

        def submit(aid: str) -> str:
            self.started(aid, lambda: self.hub.submit(self._brief_opts(job, node, aid)))
            new = self.hub.store.load_key(aid)["task"]
            self.created.append(new)
            self._recount_runs()
            self._moved(job, new, attempt)
            return f"{rule}: {job['id']} → {new} on {node}"
        return self.act(ids(rule), submit)

    def _moved(self, job: dict, new: str, attempt: int) -> None:
        self.ledger["attempts"][new] = attempt
        self.ledger["moved"][job["id"]] = new

    def _other_node(self, job: dict) -> tuple[str | None, str]:
        """Another node by automatic placement (an ``auto: false`` device takes no moved run), or the device the
        original run was explicitly placed on (``--host``); with the reasons the others were passed."""
        others = [dev for dev in self.hub.devices() if dev.name != job.get("host")]
        free_gb = int(settings.setting("min_free_gb", worker=self.hub.worker))
        dev, table = bidding.bid(others, self.hub.worker_root, repo=job.get("repo"), runner=job["runner"],
                                 access=job.get("access"), min_free_gb=free_gb)
        rows = {row["host"]: row.get("why") for row in table}
        named = next((other for other in others if other.name == self._named_host(job) and not other.auto), None)
        if dev is None and named is not None:
            dev, table = bidding.bid([named], self.hub.worker_root, repo=job.get("repo"), runner=job["runner"],
                                     manual=True, access=job.get("access"), min_free_gb=free_gb)
            rows.update({row["host"]: row.get("why") for row in table})
        for other in others:
            rows.setdefault(other.name, "only when named with --host")
        why = "; ".join(f"{host}: {reason}" for host, reason in rows.items() if reason) or "no other node"
        return (dev.name if dev else None), why

    def _named_host(self, job: dict) -> str | None:
        """The ``--host`` the original run of this to-do was handed out with (the drive's own moves name a host too,
        but that is not the owner's choice)."""
        made_from = {new: old for old, new in self.ledger["moved"].items()}
        root = job
        while root["id"] in made_from and (earlier := self.hub.store.load(made_from[root["id"]])):
            root = earlier
        return root.get("fixed_host")

    def _brief_opts(self, job: dict, node: str, aid: str) -> dict:
        return {"briefs": [job["brief"]], "mode": job["mode"], "topic": job.get("topic"), "todo": job.get("todo"),
                "bot": job["bot"], "runner": job["runner"], "model": _model(job.get("model")),
                "access": job.get("access", "workspace-write"), "repo": job.get("repo"), "host": node,
                "card": {"task": job["card"]["task"], "board": job["card"].get("board")},
                "inputs": list(job.get("inputs") or []), "wait": None, "request_key": aid,
                "base_branch": self._pushed_base(job), "note": f"goal drive: moved from {job['id']}"}

    def _pushed_base(self, job: dict) -> str | None:
        """``origin/<branch>`` when the run's branch was pushed to GitHub, else None (the repository's default)."""
        from hermes_cli.kanban_gates import GateError, gh_json
        if not self._github(job):
            return None
        try:
            gh_json("api", f"repos/{job['repo']}/branches/{job['branch']}")
        except GateError:
            return None
        return f"origin/{job['branch']}"

