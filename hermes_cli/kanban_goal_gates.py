"""Quality gates for goal-mode kanban cards.

A card may carry ``goal_gates``: a JSON list in the ``GoalGate`` shape ``/goal`` uses. The card
goal loop runs them at every turn boundary before the judge, and ``kanban_complete`` /
``kanban_request_review`` refuse while one fails. Gate execution is ``goals.run_gate``; this
module only adds the card side: the workspace, the per-card retry state and the event trail.
"""

from __future__ import annotations

import json
import sqlite3
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Iterable, Optional

from hermes_cli.goals import (
    CONTINUATION_PROMPT_GATE_FAILED_TEMPLATE,
    GATE_WAIT_EXIT_CODE,
    GoalGate,
    run_gate,
)

GATE_EVENT_KIND = "goal_gate"
_EVENT_TAIL_CHARS = 1000


def normalize_gates(value: Optional[Iterable[Any]], *, goal_mode: bool) -> Optional[str]:
    """Validate a gate list (command strings or ``GoalGate`` dicts) into the stored JSON text.

    None or an empty list stores NULL. Raises ``ValueError`` on a malformed entry, and when gates
    are given to a card that is not in goal mode (nothing would ever run them).
    """
    if value is None:
        return None
    if isinstance(value, (str, bytes, dict)):
        raise ValueError("goal_gates must be a list of shell commands or gate objects")
    gates = []
    for item in value:
        gate = GoalGate(command=item) if isinstance(item, str) else GoalGate.from_dict(item)
        gate.command = gate.command.strip()
        if not gate.command:
            raise ValueError(f"goal gate needs a non-empty command, got {item!r}")
        gates.append(gate.to_dict())
    if gates and not goal_mode:
        raise ValueError("goal gates only run on goal-mode cards; set goal_mode / --goal")
    return json.dumps(gates) if gates else None


def card_gates(task) -> list[GoalGate]:
    raw = getattr(task, "goal_gates", None)
    return [GoalGate.from_dict(d) for d in json.loads(raw)] if raw else []


@dataclass
class GateFailure:
    """Why a card's gates did not pass. ``gate`` is None when they could not run at all;
    ``waiting`` = the gate exited ``GATE_WAIT_EXIT_CODE`` (not a failure, not a retry)."""

    gate: Optional[GoalGate]
    exit_code: int
    output: str
    exhausted: bool = False
    waiting: bool = False

    def describe(self) -> str:
        if self.gate is None:
            return f"goal gates not run: {self.output}"
        tail = self.output[-_EVENT_TAIL_CHARS:] or "(no output)"
        state = "still waiting" if self.waiting else "failed"
        return f"goal gate {state} (exit {self.exit_code}): $ {self.gate.command}\n{tail}"


def _card_workspace(task, workspace: Optional[str]) -> tuple[Optional[str], Optional[str]]:
    """``(cwd, refusal)``. Gates run only in the card's own workspace: a relative gate run
    anywhere else would check a different tree and could pass a failing card."""
    declared = workspace or getattr(task, "workspace_path", None)
    if declared:
        path = Path(declared).expanduser()
        if path.is_dir():
            return str(path), None
    return None, (f"the card workspace {declared or '(none)'} is not a directory on this host, "
                  "and running gates anywhere else would check a different tree")


def _record(conn: sqlite3.Connection, task, gates: list[GoalGate], events: list[dict],
            run_id: Optional[int]) -> None:
    from hermes_cli import kanban_db as kb

    with kb.write_txn(conn):
        conn.execute("UPDATE tasks SET goal_gates = ? WHERE id = ?",
                     (json.dumps([g.to_dict() for g in gates]), task.id))
        for payload in events:
            kb._append_event(conn, task.id, GATE_EVENT_KIND, payload,
                             run_id=run_id if run_id is not None else task.current_run_id)


def run_card_gates(conn: sqlite3.Connection, task, *, workspace: Optional[str] = None,
                   count_attempts: bool = False,
                   run_id: Optional[int] = None) -> Optional[GateFailure]:
    """Run the card's gates in order, stopping at the first failure; None when all pass.

    Each run is recorded as a ``goal_gate`` event and its result is stored on the card.
    ``count_attempts`` (the goal loop) advances the per-gate retry count, which persists across
    turns; a gate past ``max_retries`` comes back ``exhausted`` with its count reset, so an
    unblocked card starts a fresh retry budget. A waiting gate (exit 75) never counts. The
    handoff tools only check (no counting).
    """
    gates = card_gates(task)
    if not gates:
        return None
    cwd, refusal = _card_workspace(task, workspace)
    if refusal:
        return GateFailure(None, -1, refusal, exhausted=True)
    failure, events = None, []
    for gate in gates:
        passed, exit_code, tail = run_gate(gate, cwd=cwd)
        gate.last_exit_code, gate.last_output_tail = exit_code, tail
        events.append({"command": gate.command, "exit_code": exit_code, "passed": passed,
                       "output_tail": tail[-_EVENT_TAIL_CHARS:]})
        if passed:
            gate.attempts = 0 if count_attempts else gate.attempts
            continue
        if exit_code == GATE_WAIT_EXIT_CODE:
            failure = GateFailure(gate, exit_code, tail, waiting=True)
            break
        if count_attempts:
            gate.attempts += 1
        failure = GateFailure(gate, exit_code, tail,
                              exhausted=count_attempts and gate.attempts > gate.max_retries)
        if failure.exhausted:
            gate.attempts = 0
        break
    _record(conn, task, gates, events, run_id)
    return failure


def handoff_gate_failure(conn: sqlite3.Connection, task) -> Optional[GateFailure]:
    """The deterministic half of every completion / review handoff (tools and CLI): a goal-mode
    card's own gates must pass in its workspace. Never fails open, judge or no judge. A worker
    scoped to the card runs them in ``$HERMES_KANBAN_WORKSPACE``; anyone else in the card's path."""
    if task is None or not task.goal_mode or not task.goal_gates:
        return None
    import os

    own = os.environ.get("HERMES_KANBAN_TASK") == task.id
    return run_card_gates(conn, task,
                          workspace=os.environ.get("HERMES_KANBAN_WORKSPACE") if own else None)


def loop_gate_check(task_id: str, goal_text: str, *, workspace: Optional[str] = None,
                    run_id: Optional[int] = None,
                    connect: Optional[Callable] = None) -> Callable[[], Optional[dict]]:
    """Gate step for ``goals.run_kanban_goal_loop``: None = no gates or all pass (judge as usual);
    ``{"prompt": ..., "reason": ...}`` = feed the gate-failed prompt and skip the judge;
    ``{"wait": ...}`` = a gate is still waiting, re-check later; ``{"block": ...}`` = retries
    exhausted or gates cannot run, so block the card naming the gate."""
    def check() -> Optional[dict]:
        from hermes_cli import kanban_db as kb
        from hermes_cli.kanban_db_connect import connect_closing

        with (connect or connect_closing)() as conn:
            task = kb.get_task(conn, task_id)
            failure = run_card_gates(conn, task, workspace=workspace, count_attempts=True,
                                     run_id=run_id) if task is not None else None
        if failure is None:
            return None
        if failure.gate is None:
            return {"block": f"Goal-mode card blocked: {failure.describe()}"}
        gate = failure.gate
        if failure.waiting:
            return {"wait": f"gate still waiting (exit {failure.exit_code}): $ {gate.command}"}
        if failure.exhausted:
            return {"block": (f"Goal-mode card blocked: gate still failing after "
                              f"{gate.max_retries} retries — {failure.describe()}")}
        return {"reason": f"gate failed (exit {failure.exit_code}): $ {gate.command}",
                "prompt": CONTINUATION_PROMPT_GATE_FAILED_TEMPLATE.format(
                    goal=goal_text, command=gate.command, exit_code=failure.exit_code,
                    attempt=gate.attempts, max_retries=gate.max_retries,
                    output=failure.output or "(no output)")}
    return check


def wait_with_heartbeat(seconds: float) -> None:
    """Waiting-gate backoff for a dispatched worker: heartbeat first so the claim (15 min TTL)
    outlives the wait (at most 10 min); the dispatcher's runtime cap still ends the card."""
    import time

    from tools.kanban_tools import heartbeat_current_worker_from_env

    heartbeat_current_worker_from_env()
    time.sleep(seconds)


def render_gates(task) -> list[str]:
    lines = []
    for i, gate in enumerate(card_gates(task), 1):
        if gate.last_exit_code is None:
            status = "not run yet"
        elif gate.last_exit_code == 0:
            status = "passing"
        elif gate.last_exit_code == GATE_WAIT_EXIT_CODE:
            status = "waiting"
        else:
            status = (f"failing (exit {gate.last_exit_code}, "
                      f"attempt {gate.attempts}/{gate.max_retries})")
        lines.append(f"  {i}. $ {gate.command}  [{status}]")
        tail = gate.last_output_tail.strip().splitlines()[-3:] if gate.last_exit_code else []
        lines.extend(f"       {line}" for line in tail)
    return lines
