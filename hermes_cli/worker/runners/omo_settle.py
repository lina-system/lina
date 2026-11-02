"""When an omo run has really settled.

``omo host session wait --until idle`` returns at the end of the first turn even while a background job of the
session still runs, so the run is judged from the session's root lifecycle (``host session state``) instead: one
``Settle.step`` per poll reads it, classifies it for the dashboard and decides whether the run ended.

Ends: the engine sealed the lifecycle (``done``, ``failed``, ``cancelled``); the turn ended after an answered question
(``idle_after_question``); nothing was recorded for the stall grace while not running, a hold or pause, or a
background item older than its cap (``stalled``); the session closed or parked; the hard timeout; an unreadable state.
The clocks live in ``settle.<run>.json`` so a restarted service resumes the same judgement; ``lifecycle.<run>.json``
is the last summary, ``progress.<run>.jsonl`` one line per change and ``wait.<run>.json`` the outcome.
"""

from __future__ import annotations

import json
import time
from datetime import datetime
from typing import Callable

from hermes_cli.worker.runners.base import Task

OmoCall = Callable[[list[str], float], dict | None]
MOVING = ("thinking", "tool", "running", "checking")
CONTRACT = {"done": "done", "idle_after_question": "done", "failed": "failed", "cancelled": "cancelled",
            "stalled": "stalled"}
LOCAL_ENDS = ("timeout", "cancelled", "stalled")  # ends the worker imposes while the session may still be working
SEALED = ("done", "failed", "cancelled")
ABORT_WAIT_S = 120.0


def parse_time(value) -> float | None:
    try:
        return datetime.fromisoformat(str(value)).timestamp()
    except (TypeError, ValueError):
        return None


def lifecycle_view(answer: dict | None) -> dict | None:
    """The parts of a ``host session state`` answer the judgement reads; None when it is not a state answer."""
    if not answer or answer.get("action") != "state":
        return None
    state = answer.get("state") or {}
    lifecycle = state.get("lifecycle")
    if not isinstance(lifecycle, dict):
        return {"no_lifecycle": True, "isStreaming": state.get("isStreaming")}
    view = {key: lifecycle.get(key) for key in ("state", "since", "seq", "completion", "completionId", "failure",
                                                "lastStop", "restoring", "persisted")}
    view["waitingOn"] = lifecycle.get("waitingOn") or {}
    view["holds"] = [hold.get("kind") for hold in lifecycle.get("holds") or []]
    for key in ("isStreaming", "isBashRunning", "isCompacting", "retryAttempt", "pendingMessageCount"):
        view[key] = state.get(key)
    view["pendingQuestions"] = len(state.get("pendingQuestions") or [])
    view["pendingDialogs"] = len(state.get("pendingDialogs") or [])
    return view


def background_items(view: dict) -> list[dict]:
    return [{"source": group.get("source"), "label": item.get("label") or item.get("id"),
             "startedAt": item.get("startedAt")}
            for group in view["waitingOn"].get("background") or [] for item in group.get("items") or []]


def _waiting(view: dict, activity: dict, now: float, background_max_s: float) -> tuple[str, str]:
    waiting = view["waitingOn"]
    active = sum(int(group.get("activeCount") or 0) for group in waiting.get("background") or [])
    children = int(waiting.get("children") or 0)
    old = [item for item in background_items(view)
           if parse_time(item["startedAt"]) and now - parse_time(item["startedAt"]) > background_max_s]
    if old:
        return "stalled", f"background older than {int(background_max_s)}s: {old[0]['label']}"
    if view.get("state") in ("waiting_background", "waiting_children") and active + children > 0:
        sources = [group.get("source") or "" for group in waiting.get("background") or []]
        last = activity.get("last_entry_at")
        if (sources and children == 0 and all(s.startswith("delivery:") for s in sources) and last
                and now - last > 60):
            return "stalled_suspect", "delivery not settled (engine stuck in waiting_background)"
        labels = [item["label"] for item in background_items(view)[:3] if item.get("label")]
        return "background", ", ".join(labels) or f"bg {active} kids {children}"
    return "stalled_suspect", f"lifecycle {view.get('state')} with nothing to wait on"


def classify(view: dict, activity: dict, now: float, background_max_s: float) -> tuple[str, str]:
    """The dashboard state of a session (first rule that matches) and why."""
    if view.get("no_lifecycle"):
        return ("running" if view.get("isStreaming") else "no_lifecycle"), ""
    state, waiting = view.get("state"), view["waitingOn"]
    if view.get("restoring") or view.get("persisted") is False:
        return "checking", "restoring or unpersisted"
    if state == "done":
        return "done", ""
    if state in ("failed", "cancelled"):
        return state, (view.get("failure") or {}).get("reason") or ""
    if state == "waiting_human" or waiting.get("human") or view.get("pendingQuestions"):
        return "human", ",".join(h.get("kind", "?") for h in waiting.get("human") or []) or "question"
    if state in ("paused", "held"):
        return "stalled", f"{state} {','.join(view.get('holds') or [])}".strip()
    if (view.get("lastStop") or {}).get("cause") == "no_wake" and state != "running":
        return "stalled", "no_wake"
    if state == "running":
        return ("tool", activity.get("last_tool") or "bash") if view.get("isBashRunning") else \
            ("thinking", "") if view.get("isStreaming") else ("running", "")
    return _waiting(view, activity, now, background_max_s)


def answered_and_idle(view: dict, task: Task) -> bool:
    """Only the engine's own ``question:<run>`` entry is left: no question or dialog open, nothing streaming or
    queued, and ``report.md`` written. The engine keeps ``waiting_human`` until a new turn comes."""
    human = view["waitingOn"].get("human") or []
    synthetic = bool(human) and all(str(h.get("requestId", "")).startswith("question:") for h in human)
    return (synthetic and not view.get("pendingQuestions") and not view.get("pendingDialogs")
            and not view.get("isStreaming") and not view.get("pendingMessageCount")
            and (task.work / "report.md").is_file())


def stopped(view: dict | None) -> bool:
    """The engine reports the session at rest: sealed, or neither running a turn, streaming nor in a bash call."""
    if view is None:
        return False
    if view.get("state") in SEALED:
        return True
    return not view.get("isStreaming") and not view.get("isBashRunning") and view.get("state") != "running"


def stop_session(call: OmoCall, ref: str, wait_s: float) -> bool:
    """Abort the session's turn and wait until the engine reports it stopped; False while it still works."""
    call(["abort", ref], 60)
    deadline = time.monotonic() + wait_s
    while True:
        if stopped(lifecycle_view(call(["state", ref], 60))):
            return True
        if time.monotonic() >= deadline:
            return False
        call(["wait", ref, "--until", "idle", "--timeout", "10000"], 30)


class Settle:
    def __init__(self, task: Task, run: int, ref: str, call: OmoCall, limits: dict):
        self.task, self.run, self.ref, self.call, self.limits = task, run, ref, call, limits
        self.file = task.path(f"settle.{run}.json")
        try:
            self.mem = json.loads(self.file.read_text(encoding="utf-8-sig"))
        except (OSError, ValueError):
            self.mem = {"start": time.time(), "quiet_since": None, "last_key": None, "errs": 0, "wakes": 0}

    def _save(self) -> None:
        self.file.write_text(json.dumps(self.mem, ensure_ascii=False), encoding="utf-8")

    def _finish(self, outcome: str, **extra) -> str:
        """End the run; an end the worker imposes first aborts the session and needs it confirmed stopped, else the
        run stays ``running`` (its slot held) and the next poll aborts again."""
        if outcome in LOCAL_ENDS and not stop_session(self.call, self.ref,
                                                      float(self.limits.get("abort_wait_s", ABORT_WAIT_S))):
            self.mem["stopping"] = {"outcome": outcome, **extra}
            self._save()
            return "running"
        result = {"outcome": outcome, "waited_s": round(time.time() - self.mem["start"], 1),
                  "wakes": self.mem["wakes"], **extra}
        self.task.path(f"wait.{self.run}.json").write_text(json.dumps(result, ensure_ascii=False), encoding="utf-8")
        self.mem["outcome"] = outcome
        self._save()
        return CONTRACT.get(outcome, "failed")

    def _activity(self) -> dict:
        """The newest entries of the session since the last poll (``read --since <cursor>``)."""
        cursor = self.mem.get("cursor")
        answer = self.call(["read", self.ref, *(["--since", cursor] if cursor else ["--tail", "200"])], 60)
        if not answer or answer.get("action") != "read":
            self.mem["cursor"] = None
        else:
            for entry in answer.get("entries") or []:
                self._note_entry(entry)
            if answer.get("nextSince"):
                self.mem["cursor"] = str(answer["nextSince"])
        return {key: self.mem.get(key) for key in ("last_entry_at", "last_entry_type", "last_tool", "last_text")}

    def _note_entry(self, entry: dict) -> None:
        at = parse_time(entry.get("timestamp"))
        if at and (self.mem.get("last_entry_at") is None or at > self.mem["last_entry_at"]):
            self.mem["last_entry_at"] = at
        message = entry.get("message") or {}
        self.mem["last_entry_type"] = entry.get("customType") or message.get("role") or entry.get("type")
        for part in message.get("content") or []:
            if not isinstance(part, dict):
                continue
            if part.get("type") == "toolCall":
                self.mem["last_tool"] = part.get("name")
            if part.get("type") == "text" and message.get("role") == "assistant" and part.get("text", "").strip():
                self.mem["last_text"] = part["text"].strip()[:300]

    def _record(self, view: dict, key: str, why: str, activity: dict, now: float) -> None:
        waiting = view["waitingOn"]
        summary = {"state": view.get("state"), "dash": key, "why": why, "since": view.get("since"),
                   "seq": view.get("seq"), "children": waiting.get("children", 0),
                   "background": [{"source": g.get("source"), "activeCount": g.get("activeCount"),
                                   "items": [i.get("label") or i.get("id") for i in g.get("items") or []][:5]}
                                  for g in waiting.get("background") or []][:12],
                   "human": [h.get("kind") for h in waiting.get("human") or []], "holds": view.get("holds"),
                   "completion": view.get("completion"), "completionId": view.get("completionId"),
                   "failure": view.get("failure"), "lastStop": view.get("lastStop"), **activity, "at": now}
        self.task.path(f"lifecycle.{self.run}.json").write_text(json.dumps(summary, ensure_ascii=False),
                                                                encoding="utf-8")
        marker = [key, summary["state"], summary["seq"]]
        if marker != self.mem.get("last_key"):
            with self.task.path(f"progress.{self.run}.jsonl").open("a", encoding="utf-8") as fh:
                fh.write(json.dumps(summary, ensure_ascii=False) + "\n")
            self.mem["last_key"] = marker

    def _quiet(self, key: str, activity: dict, now: float) -> float:
        if key in MOVING:
            self.mem["quiet_since"] = None
            return 0.0
        seen = activity.get("last_entry_at") or now
        since = self.mem.get("quiet_since")
        self.mem["quiet_since"] = seen if since is None else max(since, seen)
        return now - self.mem["quiet_since"]

    def _judge(self, view: dict, key: str, why: str, quiet: float, now: float) -> str | None:
        grace = float(self.limits.get("stall_grace_s", 600.0))
        extra = {"lifecycle": view.get("state"), "dash": key, "quiet_s": round(quiet)}
        if key in ("done", "failed", "cancelled"):
            return self._finish(key, completionId=view.get("completionId"), completion=view.get("completion"),
                                failure=view.get("failure"), lastStop=view.get("lastStop"))
        if key == "no_lifecycle":
            return self._finish("done", via="no lifecycle")
        if key == "stalled" and why.startswith("background older than"):
            return self._finish("stalled", why=why, **extra)
        if (key == "human" and answered_and_idle(view, self.task)
                and quiet >= float(self.limits.get("answered_grace_s", 60.0))):
            return self._finish("idle_after_question", why="the turn ended after its question was answered", **extra)
        if key in ("stalled", "stalled_suspect", "human") and quiet >= grace:
            return self._finish("stalled", why=why, **extra)
        if key == "background" and quiet >= grace and not background_items(view):
            return self._finish("stalled", why="no recorded activity", **extra)
        if now - self.mem["start"] > float(self.limits.get("hard_timeout_s", 43200.0)):
            return self._finish("timeout", **extra)
        return None

    def step(self) -> str:
        """One poll: ``running``, ``waiting_human`` or the run's end (``done``, ``stalled``, ``failed``, ...)."""
        if "outcome" in self.mem:
            return CONTRACT.get(self.mem["outcome"], "failed")
        if self.mem.get("stopping"):
            pending = dict(self.mem["stopping"])
            return self._finish(pending.pop("outcome"), **pending)
        if self.task.path("cancelled").exists():
            return self._finish("cancelled", via="lina worker cancel")
        if self.task.path(f"answer_abort.{self.run}").exists():
            return self._finish("stalled", dash="human", why="closed on an open question; the answer goes to the "
                                                             "next run")
        view = lifecycle_view(self.call(["state", self.ref], 60))
        if view is None:
            self.mem["errs"] += 1
            self._save()
            return self._finish("error", why="state unreadable 8 times") if self.mem["errs"] >= 8 else "running"
        self.mem["errs"] = 0
        now = time.time()
        activity = self._activity()
        key, why = classify(view, activity, now, float(self.limits.get("background_max_s", 3 * 3600.0)))
        self._record(view, key, why, activity, now)
        quiet = self._quiet(key, activity, now)
        ended = self._judge(view, key, why, quiet, now)
        if ended:
            return ended
        if key in MOVING and view.get("isStreaming"):
            self.mem["wakes"] += 1
            woke = self.call(["wait", self.ref, "--until", "idle", "--timeout", "60000"], 90) or {}
            if woke.get("outcome") in ("session_closed", "session_parked"):
                return self._finish(woke["outcome"])
        self._save()
        return "waiting_human" if key == "human" else "running"
