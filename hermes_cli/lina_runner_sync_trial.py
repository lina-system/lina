"""The hub trial of a new runner version (lina#238): one real request through the new binary, answered ``OK``.

A pin's ``trial`` is either ``{"argv": [...]}`` (a one-shot call; ``{prompt}`` and ``{dir}``, an empty temporary
folder that is also its working directory, are filled in) or ``{"acp": [...]}`` (the runner started as an ACP agent
and asked once over stdio). ``"ocx": true`` adds the second condition: the hub's OCX log has a ``200`` at or after the
trial's start, read through ``lina_models`` (``ocx.external`` is only read). The runner's output is never kept beyond
the one line that says why a trial failed, and no environment value is printed.
"""

from __future__ import annotations

import json
import queue
import subprocess
import tempfile
import time
from datetime import datetime
from pathlib import Path
from typing import Callable

from hermes_cli import lina_runners

PROMPT = "Reply with exactly OK and nothing else."
TIMEOUT_S = 300.0
OCX_LOGS = ("logs", "--json")
_LOG_LISTS = ("logs", "entries", "requests", "items", "rows")
_LOG_STATUS = ("status", "statusCode", "status_code")
_LOG_ID = ("requestId", "request_id", "id")
_LOG_MODEL = ("requestedModel", "model")
_LOG_PROTOCOL = ("inboundProtocol", "protocol")
_LOG_TIME = ("timestamp", "ts", "time", "at", "startedAt", "started_at", "createdAt", "created_at")


def said_ok(text: str) -> bool:
    return any(line.strip() == "OK" for line in (text or "").splitlines())


def _one_line(text: str) -> str:
    lines = [line.strip() for line in (text or "").splitlines() if line.strip()]
    return (lines[-1] if lines else "no output")[:160]


def _argv_trial(entry: list[str], spec: dict, env: dict, folder: Path, run) -> tuple[bool, str]:
    argv = [*entry, *(str(part).format(prompt=PROMPT, dir=folder) for part in spec["argv"])]
    try:
        done = run(argv, env=env, cwd=str(folder), stdin=subprocess.DEVNULL, capture_output=True, text=True,
                   encoding="utf-8", errors="replace", timeout=float(spec.get("timeout", TIMEOUT_S)), check=False)
    except subprocess.TimeoutExpired:
        return False, f"no answer in {spec.get('timeout', TIMEOUT_S)}s"
    except OSError as exc:
        return False, f"could not start: {exc}"
    if done.returncode != 0:
        return False, f"exit {done.returncode}: {_one_line(done.stderr or done.stdout)}"
    return (True, "answered OK") if said_ok(done.stdout) else (False, f"did not answer OK: {_one_line(done.stdout)}")


def _acp_trial(entry: list[str], spec: dict, env: dict, folder: Path, popen) -> tuple[bool, str]:
    """initialize, session/new, one session/prompt; the agent's message chunks are the answer."""
    timeout = float(spec.get("timeout", TIMEOUT_S))
    try:
        proc = popen([*entry, *spec["acp"]], env=env, cwd=str(folder), stdin=subprocess.PIPE,
                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    except OSError as exc:
        return False, f"could not start: {exc}"
    lines: queue.Queue = queue.Queue()
    def pump() -> None:
        for raw in proc.stdout:
            lines.put(raw)
        lines.put(None)

    from agent.memory_provider import spawn_context_thread
    spawn_context_thread(pump, name="lina-runner-trial-acp").start()
    deadline = time.monotonic() + timeout

    def send(message: dict) -> None:
        proc.stdin.write((json.dumps(message) + "\n").encode())
        proc.stdin.flush()

    def answer(request_id: int, chunks: list[str]) -> dict:
        while True:
            try:
                raw = lines.get(timeout=max(0.0, deadline - time.monotonic()))
            except queue.Empty as exc:
                raise TimeoutError from exc
            if raw is None:
                raise EOFError
            try:
                message = json.loads(raw)
            except ValueError:
                continue
            if message.get("id") == request_id and "method" not in message:
                if "error" in message:
                    raise RuntimeError(str(message["error"].get("message") or message["error"])[:160])
                return message.get("result") or {}
            if message.get("method") == "session/request_permission" and "id" in message:
                send({"jsonrpc": "2.0", "id": message["id"], "result": {"outcome": {"outcome": "cancelled"}}})
            update = (message.get("params") or {}).get("update") or {}
            if message.get("method") == "session/update" and update.get("sessionUpdate") == "agent_message_chunk":
                chunks.append(str((update.get("content") or {}).get("text") or ""))

    chunks: list[str] = []
    try:
        send({"jsonrpc": "2.0", "id": 1, "method": "initialize",
              "params": {"protocolVersion": 1, "clientCapabilities": {}}})
        answer(1, chunks)
        send({"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": {"cwd": str(folder), "mcpServers": []}})
        session = answer(2, chunks).get("sessionId")
        send({"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
              "params": {"sessionId": session, "prompt": [{"type": "text", "text": PROMPT}]}})
        answer(3, chunks)
    except TimeoutError:
        return False, f"no ACP answer in {timeout:g}s"
    except (EOFError, BrokenPipeError):
        return False, "the ACP agent ended before it answered"
    except RuntimeError as exc:
        return False, f"ACP error: {exc}"
    finally:
        proc.kill()
        proc.wait()
    text = "".join(chunks)
    return (True, "answered OK") if said_ok(text) else (False, f"did not answer OK: {_one_line(text)}")


def _stamp(value) -> float | None:
    if isinstance(value, (int, float)):
        return value / 1000.0 if value > 1e11 else float(value)
    if isinstance(value, str):
        try:
            return datetime.fromisoformat(value).timestamp()
        except ValueError:
            return None
    return None


def _rows(logs) -> list[dict]:
    rows = logs
    if isinstance(logs, dict):
        rows = next((logs[key] for key in _LOG_LISTS if isinstance(logs.get(key), list)), [])
    return [row for row in rows if isinstance(row, dict)] if isinstance(rows, list) else []


def _first(row: dict, keys: tuple[str, ...]):
    return next((row.get(key) for key in keys if row.get(key) is not None), None)


def request_ids(logs) -> set[str]:
    return {str(rid) for rid in (_first(row, _LOG_ID) for row in _rows(logs)) if rid is not None}


def matches(row: dict, match: dict) -> bool:
    """*row* is the trial's kind of request: every condition of the pin's ``ocx_match`` holds."""
    model = str(_first(row, _LOG_MODEL) or "")
    protocol = str(_first(row, _LOG_PROTOCOL) or "")
    return (model.find(match.get("model_contains", "")) >= 0
            and model.endswith(match.get("model_endswith", ""))
            and ("protocol" not in match or protocol == match["protocol"]))


def ocx_answered(logs, since: float, match: dict, seen: set[str]) -> tuple[bool, str]:
    """A ``200`` the trial caused: a request id that was not in the log before the trial (*seen*), of the trial's
    kind (*match*), not older than the trial's start. Other requests of the same model and protocol that arrive at
    the same time cannot be told apart from it (OCX records no client); the trial's ``OK`` is the main evidence."""
    if not isinstance(logs, (dict, list)):
        return False, "the OCX log is not a list"
    new = 0
    for row in _rows(logs):
        rid = _first(row, _LOG_ID)
        stamp = next((s for s in (_stamp(row.get(key)) for key in _LOG_TIME) if s is not None), None)
        if rid is None or str(rid) in seen or (stamp is not None and stamp < since - 1):
            continue
        new += 1
        if _first(row, _LOG_STATUS) in (200, "200") and matches(row, match):
            return True, "OCX answered 200"
    return False, f"no new 200 matching {match} in the OCX log since the trial began ({new} new entries)"


def read_ocx_logs(root: Path):
    from hermes_cli import lina_models
    return lina_models.run(OCX_LOGS, root, fresh=True)  # a cached read would hide the trial's own request


def trial(root: Path, runner: lina_runners.RunnerPin, version: str, *,
          run: Callable[..., subprocess.CompletedProcess] = subprocess.run, popen=subprocess.Popen,
          ocx_logs: Callable[[Path], object] = read_ocx_logs, clock: Callable[[], float] = time.time) -> tuple[bool, str]:
    spec = runner.trial
    if not spec:
        return False, "no trial in the pin"
    seen: set[str] = set()
    if spec.get("ocx"):
        if not isinstance(spec.get("ocx_match"), dict) or not spec["ocx_match"]:
            return False, "no ocx_match in the pin"
        try:
            seen = request_ids(ocx_logs(root))
        except lina_runners.OcxError as exc:
            return False, f"OCX log could not be read before the trial: {exc}"
    entry = lina_runners.command(root, runner, version)
    env = lina_runners.run_env(root, runner)
    began = clock()
    with tempfile.TemporaryDirectory(prefix=f"lina-trial-{runner.name}-") as folder:
        if "acp" in spec:
            ok, why = _acp_trial(entry, spec, env, Path(folder), popen)
        else:
            ok, why = _argv_trial(entry, spec, env, Path(folder), run)
    if not ok or not spec.get("ocx"):
        return ok, why
    try:
        logs = ocx_logs(root)
    except lina_runners.OcxError as exc:
        return False, f"answered OK but the OCX log could not be read: {exc}"
    return ocx_answered(logs, began, spec["ocx_match"], seen)
