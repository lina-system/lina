"""Run-scoped ACP clients; disk handles and an inbox are the cross-process control plane.

The I/O thread belongs to ``worker _run``. Only it writes ACP stdin and the status
snapshot. Send/answer processes enqueue under the same lock used to finish a turn,
so accepted input cannot disappear between a terminal-state check and shutdown.
"""

from __future__ import annotations

import contextlib
import json
import os
import selectors
import signal
import subprocess
import threading
import time
import uuid
from pathlib import Path

from agent.memory_provider import spawn_context_thread
from hermes_cli.lina_work_settings import ACCESS
from hermes_cli.worker.runners.access import recorded_access
from hermes_cli.worker.runners.base import Handle, ModelProfile, RunnerError, Task, standard_result
from hermes_cli.worker.runners.command import CommandRunner, FINAL_MAX
from hermes_platform.resolver import locate_command
from tools.environments.local import build_subprocess_env

FLAVORS = {
    "grok": ("grok", "agent", "stdio"),
    "dsh": ("dsh", "acp"),
    "agy": ("agy", "acp"),
    "claude": ("claude-agent-acp",),
    "codex": ("codex-acp",),
}
_BYPASS = {"--always-approve", "--dangerously-skip-permissions", "--yolo",
           "--dangerously-bypass-approvals-and-sandbox", "--skip-permissions"}
_QUESTION = "If you need a human answer, write question.md in the working directory and end your turn."


def _json(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8-sig")) if path.exists() else {}


def _publish(path: Path, data: dict) -> None:
    temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    temporary.write_text(json.dumps(data), encoding="utf-8")
    os.replace(temporary, path)


@contextlib.contextmanager
def _lock(handle: Handle):
    import fcntl
    with handle.task.path(f"acp.{handle.run}.lock").open("a") as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)  # windows-footgun: ok — POSIX work devices only
        try:
            yield
        finally:
            fcntl.flock(stream, fcntl.LOCK_UN)  # windows-footgun: ok — POSIX work devices only


def _status_path(handle: Handle) -> Path:
    return handle.task.path(f"acp.{handle.run}.json")


def _inbox(handle: Handle) -> Path:
    return handle.task.path(f"acp-inbox.{handle.run}")


def _inside(work: Path, target: str) -> bool:
    if not target or any(char in target for char in ("\x00", "\n", "\r")):
        return False
    try:
        return (work / target).resolve().is_relative_to(work.resolve())
    except (OSError, ValueError, RuntimeError):
        return False


def _permission(work: Path, mode: str, call: dict) -> tuple[bool, list[str]]:
    raw = call.get("rawInput") or {}
    if not isinstance(raw, dict):
        raw = {}
    locations = call.get("locations") or []
    targets = [location.get("path") for location in locations if isinstance(location, dict)]
    targets += [raw[key] for key in ("path", "file_path", "filePath", "target_path") if key in raw]
    malformed = len(targets) < len(locations) or any(not isinstance(target, str) or not target for target in targets)
    targets = [target for target in targets if isinstance(target, str)]
    if mode == "full":
        return True, targets
    if mode in ("read-only", "workspace-write") and call.get("kind") in ("read", "search", "fetch", "think"):
        return True, targets
    if mode != "workspace-write":
        return False, targets
    if call.get("kind") in ("edit", "delete"):
        return not malformed and bool(targets) and all(_inside(work, target) for target in targets), targets
    if call.get("kind") == "execute":
        cwd = raw.get("cwd")
        return isinstance(cwd, str) and bool(cwd.strip()) and _inside(work, cwd), [str(cwd)]
    return False, targets


class AcpRunner:
    def __init__(self, name: str, spec: dict, limits: dict | None = None):
        argv = spec.get("argv")
        if not isinstance(argv, list) or not argv or not all(isinstance(arg, str) and arg for arg in argv):
            raise RunnerError(f"runner {name}: `argv` must be a non-empty list of strings")
        flavor = spec.get("flavor")
        if flavor is not None and (not isinstance(flavor, str) or flavor not in FLAVORS):
            raise RunnerError(f"runner {name}: unknown ACP flavor {flavor!r}")
        if any(arg.split("=", 1)[0] in _BYPASS for arg in argv):
            raise RunnerError(f"runner {name}: automatic approval flags bypass ACP access policy")
        self.name, self.argv, self.flavor = name, list(argv), flavor
        self.env = {str(key): str(value) for key, value in (spec.get("env") or {}).items()}
        self.limits = limits or {}
        self._sessions: dict[int, _Session] = {}

    def available(self) -> tuple[bool, str]:
        found = locate_command(self.argv[0]).command
        return (True, found[0]) if found else (False, f"{self.argv[0]} not found on PATH")

    def honours(self, mode: str) -> tuple[bool, str]:
        return mode in ACCESS, "" if mode in ACCESS else f"unknown access mode {mode!r}"

    def _launch(self, task: Task, run: int, prompt: str, model: ModelProfile | None,
                access: str, previous: dict | None = None) -> Handle:
        supported, reason = self.honours(access)
        if not supported:
            raise RunnerError(reason)
        handle = Handle(task, run, {"runner": self.name, "access": access, "since": time.time(),
                                    "owner_pid": os.getpid(), "model": model.as_dict() if model else None})
        _inbox(handle).mkdir()
        session = _Session(self, handle)
        with task.path(f"stderr.{run}.txt").open("ab") as err:
            session.proc = subprocess.Popen(self.argv, cwd=task.work, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                            # detached-spawn: ok — awaited with group-killing cleanup on every ending
                                            stderr=err, start_new_session=True,
                                            env=build_subprocess_env(scrub_secrets=False, inherit_profile_home=False,
                                                                     extra=self.env))
        handle.data["pid"] = session.proc.pid
        handle.save()
        session.publish("running")
        task.event(run, "launched", argv=self.argv[:1], flavor=self.flavor)
        self._sessions[run] = session
        session.thread = spawn_context_thread(session.run, args=(prompt, previous or {}), name=f"acp-{run}", daemon=False)
        session.thread.start()
        return handle

    def start(self, task: Task, prompt: str, model: ModelProfile | None,
              access: str = "workspace-write") -> Handle:
        return self._launch(task, 1, prompt, model, access)

    def _enqueue(self, handle: Handle, kind: str, text: str = "", access: str | None = None) -> bool:
        with _lock(handle):
            status = _json(_status_path(handle))
            if status.get("state") not in ("running", "waiting_human"):
                return False
            if kind == "answer" and status.get("state") != "waiting_human":
                return False
            if kind == "answer" and any(_inbox(handle).iterdir()):
                return False
            _publish(_inbox(handle) / f"{time.time_ns()}-{uuid.uuid4().hex}.json",
                     {"kind": kind, "text": text, "access": access})
            return True

    def follow_up(self, handle: Handle, text: str, *, run: int | None = None,
                  access: str | None = None) -> tuple[str, Handle]:
        mode = recorded_access(handle, access)
        if run is None and self._enqueue(handle, "steer", text, mode):
            return "steered", handle
        if self.state(handle) in ("running", "waiting_human"):
            return "refused", handle
        previous = _json(_status_path(handle))
        return "started", self._launch(handle.task, run or handle.run + 1, text,
                                        ModelProfile.from_dict(handle.data.get("model")), mode, previous)

    def answer(self, handle: Handle, text: str) -> dict:
        mode = recorded_access(handle)
        if not self._enqueue(handle, "answer", text, mode):
            return {"action": "refuse", "reason": "no_question"}
        return {"action": "answer", "steps": 1, "still_pending": 0}

    def state(self, handle: Handle) -> str:
        status = _json(_status_path(handle))
        state = status.get("state", "failed")
        if state in ("running", "waiting_human"):
            try:
                os.kill(handle.data["owner_pid"], 0)  # windows-footgun: ok — POSIX work devices only
            except ProcessLookupError:
                CommandRunner.stop(self, handle)
                return "failed"
            if handle.task.path("cancelled").exists():
                self.stop(handle)
                return "cancelled"
            with _lock(handle):
                status = _json(_status_path(handle))
                state = status.get("state", "failed")
                if state == "waiting_human" and any(_inbox(handle).iterdir()):
                    return "running"
        return "running" if state == "closing" else state

    def result(self, handle: Handle) -> dict[str, Path]:
        session = self._sessions.get(handle.run)
        if session is not None:
            session.thread.join(timeout=10)
        return standard_result(handle.task)

    def stop(self, handle: Handle) -> None:
        self._enqueue(handle, "stop")
        session = self._sessions.get(handle.run)
        if session is not None:
            session.thread.join(timeout=5)
        else:
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline and _json(_status_path(handle)).get("state") in ("running", "waiting_human"):
                time.sleep(0.05)
        CommandRunner.stop(self, handle)
        if session is not None:
            session.thread.join(timeout=5)


class _Session:
    """Mutable state owned by one I/O thread, never used by a send/answer process."""

    def __init__(self, runner: AcpRunner, handle: Handle):
        self.runner, self.handle = runner, handle
        self.proc: subprocess.Popen
        self.thread: threading.Thread
        self.session_id = ""
        self.capabilities: dict = {}
        self.grok_hooks = False
        self.calls: dict[str, dict] = {}
        self.responses: dict[int, dict] = {}
        self.sequence = 0
        self.buffer = b""
        self.last_activity = time.monotonic()
        self.final = ""
        self.state = "running"
        self.stopping = False
        self.selector = selectors.DefaultSelector()

    def publish(self, state: str) -> None:
        self.state = state
        _publish(_status_path(self.handle), {"state": state, "session_id": self.session_id,
                                            "capabilities": self.capabilities, "access": self.handle.data["access"]})

    def send(self, message: dict) -> None:
        self.proc.stdin.write((json.dumps({"jsonrpc": "2.0", **message}) + "\n").encode())
        self.proc.stdin.flush()

    def request(self, method: str, params: dict) -> int:
        self.sequence += 1
        self.send({"id": self.sequence, "method": method, "params": params})
        return self.sequence

    def rpc(self, method: str, params: dict) -> dict:
        request_id = self.request(method, params)
        while request_id not in self.responses:
            self.pump()
            self.check_stall()
        message = self.responses.pop(request_id)
        if "error" in message:
            raise RunnerError(f"ACP {method}: {json.dumps(message['error'], ensure_ascii=False)}")
        result = message.get("result")
        if not isinstance(result, dict):
            raise RunnerError(f"ACP {method}: result must be an object")
        return result

    def pump(self) -> None:
        for key, _ in self.selector.select(0.05):
            chunk = os.read(key.fd, 65536)
            if not chunk:
                raise RunnerError("ACP agent closed stdout before the run finished")
            self.last_activity = time.monotonic()
            self.buffer += chunk
            if len(self.buffer) > 8 * 1024 * 1024:
                raise RunnerError("ACP message exceeds 8 MiB")
            while b"\n" in self.buffer:
                line, self.buffer = self.buffer.split(b"\n", 1)
                if line.strip():
                    self.message(json.loads(line))

    def message(self, message: dict) -> None:
        if not isinstance(message, dict) or message.get("jsonrpc") != "2.0":
            raise RunnerError("invalid ACP JSON-RPC message")
        method, params = message.get("method"), message.get("params", {})
        if method == "session/request_permission" and "id" in message:
            self.permission(message["id"], params)
        elif method in ("x.ai/hooks/run", "_x.ai/hooks/run") and "id" in message and self.grok_hooks:
            self.grok_hook(message["id"], params)
        elif method == "session/update":
            if params.get("sessionId") != self.session_id:
                return
            update = params.get("update", {})
            kind = update.get("sessionUpdate")
            if kind in ("tool_call", "tool_call_update"):
                call_id = update.get("toolCallId", "")
                self.calls.setdefault(call_id, {}).update(update)
            elif kind == "agent_message_chunk" and update.get("content", {}).get("type") == "text":
                text = update["content"]["text"]
                self.final = (self.final + text)[-FINAL_MAX:]
                with self.handle.task.path(f"stdout.{self.handle.run}.txt").open("a", encoding="utf-8") as stream:
                    stream.write(text)
        elif method and "id" in message:
            self.send({"id": message["id"], "error": {"code": -32601, "message": "Client method not supported"}})
        elif not method and "id" in message:
            self.responses[message["id"]] = message

    def permission(self, request_id: int | str, params: dict) -> None:
        update = params.get("toolCall", {})
        call = {**self.calls.get(update.get("toolCallId", ""), {}), **update}
        mode = self.access_mode()
        allow, targets = _permission(self.handle.task.work, mode, call)
        allow = allow and params.get("sessionId") == self.session_id and not self.stopping
        wanted = "allow_once" if allow else "reject_once"
        option = next((item for item in params.get("options", []) if item.get("kind") == wanted), None)
        allowed = allow and option is not None
        outcome = {"outcome": "selected", "optionId": option["optionId"]} if option else {"outcome": "cancelled"}
        self.handle.task.event(self.handle.run, "permission_allowed" if allowed else "permission_denied",
                               tool=call.get("title") or call.get("kind", "unknown"), targets=targets, mode=mode)
        self.send({"id": request_id, "result": {"outcome": outcome}})

    def access_mode(self) -> str:
        """Every permission channel observes a job narrowed by another process."""
        mode = self.handle.data["access"]
        # A live job may have been narrowed by the device after the inbox was sent.
        job_mode = _json(self.handle.task.path("job.json")).get("access")
        if job_mode in ACCESS and ACCESS.index(job_mode) < ACCESS.index(mode):
            mode = job_mode
            self.handle.data["access"] = mode
            self.handle.save()
        return mode

    def check_stall(self) -> None:
        if time.monotonic() - self.last_activity >= float(self.runner.limits.get("stall_grace_s", 600)):
            self.state = "stalled"
            raise RunnerError("ACP agent exceeded stall_grace_s")

    def grok_hook(self, request_id: int | str, params: dict) -> None:
        """Grok's default native tools may skip request_permission; gate every pre-tool hook."""
        name = params.get("toolName", "unknown")
        raw = params.get("toolInput", {})
        kinds = {"write_file": "edit", "create_file": "edit", "edit_file": "edit",
                 "str_replace_editor": "edit", "search_replace": "edit",
                 "write": "edit", "run_terminal_command": "execute", "bash": "execute",
                 "read_file": "read", "read": "read", "list_dir": "read",
                 "grep": "search", "glob": "search", "search": "search", "web_search": "search",
                 "web_fetch": "fetch", "fetch": "fetch", "think": "think"}
        if kinds.get(name) == "execute" and isinstance(raw, dict) and "cwd" not in raw:
            raw = {**raw, "cwd": params.get("cwd")}
        call = {"kind": kinds.get(name, "other"), "rawInput": raw, "title": name}
        mode = self.access_mode()
        allow, targets = _permission(self.handle.task.work, mode, call)
        allow = (allow and params.get("sessionId") == self.session_id and not params.get("toolInputTruncated")
                 and params.get("hookCallbackId") == "lina-access" and params.get("hookEventName") == "pre_tool_use"
                 and not self.stopping)
        self.handle.task.event(self.handle.run, "permission_allowed" if allow else "permission_denied",
                               tool=name, targets=targets, mode=mode, channel="grok_hook")
        self.send({"id": request_id, "result": {"decision": "allow" if allow else "deny",
                                                "reason": "LINA run access policy"}})

    def open(self, previous: dict) -> str:
        initialized = self.rpc("initialize", {"protocolVersion": 1,
                               "clientInfo": {"name": "lina-worker", "version": "1"},
                               "clientCapabilities": {"fs": {"readTextFile": False, "writeTextFile": False},
                                                      "terminal": False}})
        if initialized.get("protocolVersion") != 1:
            raise RunnerError("ACP agent did not negotiate protocolVersion 1")
        self.capabilities = initialized.get("agentCapabilities", {})
        params = {"cwd": str(self.handle.task.work.resolve()), "mcpServers": []}
        if self.runner.flavor == "grok":
            hooks = self.capabilities.get("_meta", {}).get("x.ai/hooks", {})
            if "pre_tool_use" not in hooks.get("blockingEvents", []):
                raise RunnerError("grok requires blocking pre_tool_use hooks to enforce access")
            self.grok_hooks = True
            params["_meta"] = {"x.ai/hooks": {"PreToolUse": [{"matcher": ".*", "hookCallbackIds": ["lina-access"]}]}}
        previous_id = previous.get("session_id")
        method = "session/new"
        if previous_id and self.capabilities.get("loadSession"):
            method = "session/load"
        elif previous_id and "resume" in self.capabilities.get("sessionCapabilities", {}):
            method = "session/resume"
        if method != "session/new":
            params["sessionId"] = previous_id
            self.session_id = previous_id
        opened = self.rpc(method, params)
        self.session_id = opened.get("sessionId", self.session_id)
        if not self.session_id:
            raise RunnerError("ACP session/new returned no sessionId")
        self.handle.task.event(self.handle.run, "session_opened", method=method, session_id=self.session_id)
        self.publish("running")
        if previous_id and method == "session/new":
            pieces = []
            for path in (self.handle.task.path("final.md"), self.handle.task.work / "report.md"):
                if path.is_file():
                    pieces.append(path.read_text(encoding="utf-8-sig", errors="replace")[-FINAL_MAX:])
            self.handle.task.event(self.handle.run, "session_fallback", reason="load/resume not supported")
            return "Previous run context:\n" + "\n".join(pieces) + "\n\n"
        return ""

    def controls(self) -> list[dict]:
        with _lock(self.handle):
            paths = sorted(_inbox(self.handle).glob("*.json"))
            messages = [_json(path) for path in paths]
            for path in paths:
                path.unlink()
            if messages:
                self.publish("running")
            return messages

    def prompt(self, text: str) -> int:
        self.final = ""
        self.last_activity = time.monotonic()
        return self.request("session/prompt", {"sessionId": self.session_id,
                                               "prompt": [{"type": "text", "text": text + "\n\n" + _QUESTION}]})

    def turns(self, text: str) -> str:
        pending = self.prompt(text)
        queued: list[str] = []
        cancelling = False
        while True:
            for control in self.controls():
                mode = recorded_access(self.handle, control.get("access"))
                self.handle.data["access"] = mode
                self.handle.save()
                if control["kind"] == "stop":
                    self.stopping = True
                else:
                    queued.append(control["text"])
                    self.handle.task.event(self.handle.run, "input_received", action=control["kind"], session_id=self.session_id)
                if pending and not cancelling:
                    self.send({"method": "session/cancel", "params": {"sessionId": self.session_id}})
                    cancelling = True
            if self.stopping:
                return "cancelled"
            if pending in self.responses:
                message = self.responses.pop(pending)
                if "error" in message:
                    raise RunnerError(f"ACP session/prompt: {json.dumps(message['error'], ensure_ascii=False)}")
                reason = message.get("result", {}).get("stopReason")
                self.handle.task.event(self.handle.run, "turn_ended", stop_reason=reason)
                self.handle.task.path("final.md").write_text(self.final + "\n", encoding="utf-8")
                pending = None
                if reason != "end_turn" and not (reason == "cancelled" and cancelling):
                    return "cancelled" if reason == "cancelled" else "failed"
            if pending is None:
                if queued:
                    question = self.handle.task.work / "question.md"
                    if question.exists():
                        question.rename(self.handle.task.path(f"question.{self.handle.run}.{self.sequence}.md"))
                    pending = self.prompt("\n\n".join(queued))
                    queued.clear()
                    cancelling = False
                else:
                    with _lock(self.handle):
                        if any(_inbox(self.handle).iterdir()):
                            continue
                        if not (self.handle.task.work / "question.md").exists():
                            self.publish("closing")
                            return "done"
                        self.publish("waiting_human")
            self.pump()
            self.check_stall()

    def cleanup(self) -> None:
        try:
            if self.proc.poll() is None and self.session_id:
                with contextlib.suppress(OSError, ValueError):
                    self.send({"method": "session/cancel", "params": {"sessionId": self.session_id}})
                    if "close" in self.capabilities.get("sessionCapabilities", {}):
                        self.request("session/close", {"sessionId": self.session_id})
        finally:
            with contextlib.suppress(OSError, ValueError):
                self.proc.stdin.close()
            try:
                try:
                    self.proc.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    with contextlib.suppress(ProcessLookupError):
                        os.killpg(self.proc.pid, signal.SIGTERM)  # windows-footgun: ok — POSIX work devices only
                    with contextlib.suppress(subprocess.TimeoutExpired):
                        self.proc.wait(timeout=1)
            finally:
                # A grandchild can outlive an already exited group leader.
                with contextlib.suppress(ProcessLookupError):
                    os.killpg(self.proc.pid, signal.SIGKILL)  # windows-footgun: ok — POSIX work devices only
                self.proc.wait()
                with contextlib.suppress(OSError, ValueError):
                    self.proc.stdout.close()
                with contextlib.suppress(OSError, ValueError):
                    self.selector.close()

    def run(self, prompt: str, previous: dict) -> None:
        state = "failed"
        try:
            self.selector.register(self.proc.stdout, selectors.EVENT_READ)
            prefix = self.open(previous)
            state = self.turns(prefix + prompt)
        except (RunnerError, OSError, ValueError, TypeError, KeyError) as exc:
            state = "stalled" if self.state == "stalled" else "failed"
            self.handle.task.event(self.handle.run, "acp_error", error=str(exc))
            with self.handle.task.path(f"stderr.{self.handle.run}.txt").open("a", encoding="utf-8") as stream:
                stream.write(f"ACP: {exc}\n")
        finally:
            try:
                self.cleanup()
            finally:
                with _lock(self.handle):
                    self.publish(state)
