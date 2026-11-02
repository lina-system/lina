"""Quality gates for goal-mode kanban cards.

A card may carry ``goal_gates``: a JSON list in the ``GoalGate`` shape ``/goal`` uses. The card
goal loop runs them at every turn boundary before the judge, and ``kanban_complete`` /
``kanban_request_review`` refuse while one fails. The card side lives here: the workspace, the
per-card retry state, the event trail, and execution. Card gates can be written by a model
(``kanban_create goal_gates``), so unlike ``/goal``'s operator-typed gates (``goals.run_gate``)
they never run as a host shell: they go through the profile's configured terminal backend with
its scrubbed child environment, exactly like a terminal-tool command.
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
)

GATE_EVENT_KIND = "goal_gate"
_EVENT_TAIL_CHARS = 1000
_PROMPT_TAIL_CHARS = 3000


def _gate_cwd(config: dict, cwd: str, key: str) -> tuple[str, Optional[str]]:
    """Resolve the card's workspace with the terminal's shared and per-session mount policy."""
    import os

    from tools.terminal_scope import get_terminal_scope, reset_terminal_scope, set_terminal_scope
    from tools.terminal_tool import (
        _is_container_backend, _is_unusable_container_cwd, _resolve_config_cwd,
        _resolve_task_host_cwd, clear_task_env_overrides, register_task_env_overrides,
    )

    scope = get_terminal_scope()
    token = set_terminal_scope({**(scope if scope is not None else os.environ), "TERMINAL_CWD": cwd})
    try:
        configured_cwd, configured_host = _resolve_config_cwd(
            config["env_type"], config.get("docker_mount_cwd_to_workspace", False))
    finally:
        reset_terminal_scope(token)
    register_task_env_overrides(key, {"cwd": cwd, "cwd_source": "session"})
    try:
        host_cwd = _resolve_task_host_cwd({**config, "host_cwd": configured_host}, key)
    finally:
        clear_task_env_overrides(key)
    if _is_container_backend(config["env_type"]) and _is_unusable_container_cwd(cwd, mounted_host=host_cwd):
        cwd = "/workspace" if host_cwd else configured_cwd
    return cwd, host_cwd


def run_card_gate(gate: GoalGate, *, cwd: str, task_id: str) -> tuple[bool, int, str]:
    """Run one card gate in the profile's terminal backend (local, container, ssh, ...), never
    as a host shell: the backend's child environment carries no provider credentials, and a
    sandboxed backend confines the command like any model-issued shell command (SECURITY.md,
    terminal-backend isolation). A backend that cannot be brought up fails the gate closed.
    Returns ``(passed, exit_code, output_tail)``."""
    from tools.terminal_tool import _get_env_config, _select_image, resolve_task_overrides
    from tools.terminal_tool_lifecycle import _cleanup_env, _create_configured_env

    key = f"kanban-gate-{task_id}"
    env_type = "terminal"
    try:
        config = _get_env_config()
        env_type = config["env_type"]
        cwd, host_cwd = _gate_cwd(config, cwd, key)
        env = _create_configured_env(
            config, env_type, image=_select_image(env_type, resolve_task_overrides(key), config),
            cwd=cwd, timeout=int(gate.timeout_seconds), task_id=key, host_cwd=host_cwd)
    except Exception as exc:  # health: allow BLE001 -- any backend bring-up failure must fail the gate closed, and the reason is the gate's output
        return False, -1, f"[gate not run: the {env_type} backend could not be reached: {exc}]"
    try:
        result = env.execute(gate.command, cwd=cwd, timeout=int(gate.timeout_seconds), bounded_capture=True)
    except Exception as exc:  # health: allow BLE001 -- a backend error mid-run fails the gate closed with its reason as output
        return False, -1, f"[gate not run: the {env_type} backend failed: {exc}]"
    finally:
        _cleanup_env(env)
    exit_code = int(result.get("returncode", -1))
    return exit_code == 0, exit_code, str(result.get("output") or "")[-_PROMPT_TAIL_CHARS:]


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
    ``waiting`` = the gate exited ``GATE_WAIT_EXIT_CODE`` (not a failure, not a retry);
    ``superseded`` = the card changed runs while they ran, so the results were discarded."""

    gate: Optional[GoalGate]
    exit_code: int
    output: str
    exhausted: bool = False
    waiting: bool = False
    superseded: bool = False

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


def _stored_runs(task) -> list[Optional[int]]:
    """The run that produced each stored gate result (None = never run, or a pre-run-id row)."""
    raw = getattr(task, "goal_gates", None)
    return [d.get("run_id") for d in json.loads(raw)] if raw else []


def _record(conn: sqlite3.Connection, task_id: str, gates: list[GoalGate], events: list[dict],
            run_id: Optional[int]) -> bool:
    """Store the results of the run that executed the gates, fenced like the lifecycle
    mutations: if the card moved to another run (reclaimed, re-claimed) while the gates ran,
    nothing is written and False is returned, so a stale run never overwrites its successor's
    results or retry counters. Each stored gate carries ``run_id`` so readers can tell whose
    result it is; each event carries the same ``run_id``."""
    from hermes_cli import kanban_db as kb

    with kb.write_txn(conn):
        row = conn.execute("SELECT current_run_id FROM tasks WHERE id = ?", (task_id,)).fetchone()
        if row is None or row["current_run_id"] != run_id:
            return False
        conn.execute("UPDATE tasks SET goal_gates = ? WHERE id = ?",
                     (json.dumps([{**g.to_dict(), "run_id": run_id} for g in gates]), task_id))
        for payload in events:
            kb._append_event(conn, task_id, GATE_EVENT_KIND, payload, run_id=run_id)
    return True


def run_card_gates(conn: sqlite3.Connection, task, *, workspace: Optional[str] = None,
                   count_attempts: bool = False,
                   run_id: Optional[int] = None) -> Optional[GateFailure]:
    """Run the card's gates in order, stopping at the first failure; None when all pass.

    Each run is recorded as a ``goal_gate`` event and its result is stored on the card.
    ``count_attempts`` (the goal loop) advances the per-gate retry count, which persists across
    turns; a gate past ``max_retries`` comes back ``exhausted`` with its count reset, so an
    unblocked card starts a fresh retry budget. A waiting gate (exit 75) never counts. The
    handoff tools only check (no counting). Retry counts belong to one run: a successor run starts
    from zero. ``run_id`` defaults to the card's current run when the gates start.
    """
    gates = card_gates(task)
    if not gates:
        return None
    run_id = run_id if run_id is not None else task.current_run_id
    if count_attempts:
        for gate, owner in zip(gates, _stored_runs(task)):
            gate.attempts = gate.attempts if owner == run_id else 0
    cwd, refusal = _card_workspace(task, workspace)
    if refusal:
        return GateFailure(None, -1, refusal, exhausted=True)
    failure, events = None, []
    for gate in gates:
        passed, exit_code, tail = run_card_gate(gate, cwd=cwd, task_id=task.id)
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
    if not _record(conn, task.id, gates, events, run_id):
        return GateFailure(None, -1, (f"card {task.id} left run {run_id} while its gates ran; "
                                      "the results were discarded"), superseded=True)
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


def _block_end(message: str) -> dict:
    return {"end": "blocked_gate", "reason": message, "block": message}


def loop_gate_check(task_id: str, goal_text: str, *, workspace: Optional[str] = None,
                    run_id: Optional[int] = None,
                    connect: Optional[Callable] = None) -> Callable[[], Optional[dict]]:
    """Gate step for ``goals.run_kanban_goal_loop``: None = no gates or all pass (judge as usual);
    ``{"prompt": ..., "reason": ...}`` = feed the gate-failed prompt and skip the judge;
    ``{"wait": ...}`` = a gate is still waiting, re-check later; ``{"end": outcome, "reason": ...,
    "block": ...}`` = retries exhausted or gates cannot run, so block the card naming the gate;
    ``{"end": "stopped", ...}`` without ``block`` = this run no longer owns the card, so the loop
    ends without touching it."""
    def check() -> Optional[dict]:
        from hermes_cli import kanban_db as kb
        from hermes_cli.kanban_db_connect import connect_closing

        with (connect or connect_closing)() as conn:
            task = kb.get_task(conn, task_id)
            failure = run_card_gates(conn, task, workspace=workspace, count_attempts=True,
                                     run_id=run_id) if task is not None else None
        if failure is None:
            return None
        if failure.superseded:
            return {"end": "stopped", "reason": failure.output}
        if failure.gate is None:
            return _block_end(f"Goal-mode card blocked: {failure.describe()}")
        gate = failure.gate
        if failure.waiting:
            return {"wait": f"gate still waiting (exit {failure.exit_code}): $ {gate.command}"}
        if failure.exhausted:
            return _block_end(f"Goal-mode card blocked: gate still failing after "
                              f"{gate.max_retries} retries — {failure.describe()}")
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
    for i, (gate, owner) in enumerate(zip(card_gates(task), _stored_runs(task)), 1):
        if gate.last_exit_code is None:
            status = "not run yet"
        elif gate.last_exit_code == 0:
            status = "passing"
        elif gate.last_exit_code == GATE_WAIT_EXIT_CODE:
            status = "waiting"
        else:
            status = (f"failing (exit {gate.last_exit_code}, "
                      f"attempt {gate.attempts}/{gate.max_retries})")
        run = f", run {owner}" if owner is not None else ""
        lines.append(f"  {i}. $ {gate.command}  [{status}{run}]")
        tail = gate.last_output_tail.strip().splitlines()[-3:] if gate.last_exit_code else []
        lines.extend(f"       {line}" for line in tail)
    return lines
