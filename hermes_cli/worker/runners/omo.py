"""The omo runner (default): one task is one session on the device's shared omo daemon, through LINA's pinned omo.

``bin/omo`` (lina#34) runs every ``omo daemon run`` / ``omo host session …`` call; the two calls the CLI has no verb
for go over the daemon's RPC socket with small JS helpers run on the pinned Node, importing the omo engine from the
pinned vendor path (never a global install): ``omo_open.mjs`` opens a session with an explicit main model and
thinking level, ``omo_answer.mjs`` answers the question the session waits on. omo's own home stays ``~/.omo``.

```yaml
worker:
  runners:
    omo: {type: omo}   # built in; `bin`, `node` and `engine` override the pinned paths (tests use a fake omo)
```
"""

from __future__ import annotations

import json
import os
import subprocess
import tempfile
import time
from pathlib import Path

from hermes_cli.worker.runners.base import Handle, ModelProfile, RunnerError, Task, standard_result
from hermes_cli.worker.runners.access import recorded_access
from hermes_cli.lina_work_settings import ACCESS
from hermes_cli.worker.runners.omo_settle import Settle, classify, lifecycle_view, parse_time, stop_session
from hermes_cli.worker.runners.omo_processes import stop_task_processes
from tools.environments.local import build_subprocess_env

HELPERS = Path(__file__).resolve().parent
STOP_WAIT_S = 60.0  # within the hub's 120 s for a `_device cancel`
ENGINE = Path("node_modules") / "@code-yeongyu" / "senpi"


def _last_json(raw: bytes) -> dict | None:
    lines = raw.decode("utf-8", "replace").strip().splitlines()
    try:
        value = json.loads(lines[-1]) if lines else None
    except ValueError:
        return None
    return value if isinstance(value, dict) else None


class OmoRunner:
    def __init__(self, name: str, spec: dict, limits: dict | None = None, root: Path | None = None):
        from hermes_cli import lina_deps, lina_omo
        self.name, self.limits = name, limits or {}
        base = self.root = root or lina_deps.lina_root()
        self.bin = Path(spec.get("bin") or lina_omo.shim_path(base))
        self.node = Path(spec.get("node") or lina_omo.node_bin(base))
        current = lina_omo.vendor_dir(base) / "current"
        candidates = [current / ENGINE, current / "node_modules" / lina_omo.omo_pin().package / ENGINE]
        self.engine = Path(spec["engine"]) if spec.get("engine") else next(
            (each for each in candidates if each.is_dir()), candidates[0])
        # The omo CLI and the RPC helpers are clients of the device's own daemon over its socket: the model
        # credentials live with the daemon and magpie, so this client gets none of them.
        self.env = build_subprocess_env(scrub_secrets=True, extra={"OMO_RUNTIME": "node", "GIT_TERMINAL_PROMPT": "0",
                                                                   "GCM_INTERACTIVE": "never"})

    def _call(self, argv: list[str], timeout: float, stderr: Path | None = None) -> tuple[dict | None, int]:
        """One omo call with its answer read from a file: through a pipe omo stops after the first 64 KiB."""
        with tempfile.TemporaryFile() as out, open(stderr or os.devnull, "ab") as err:
            try:
                rc = subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=out, stderr=err, env=self.env,
                                    timeout=timeout, check=False).returncode
            except (OSError, subprocess.TimeoutExpired):
                return None, 124
            out.seek(0)
            return _last_json(out.read()), rc

    def omo(self, args: list[str], timeout: float, stderr: Path | None = None) -> tuple[dict | None, int]:
        return self._call([str(self.bin), *args], timeout, stderr)

    def session(self, args: list[str], timeout: float) -> dict | None:
        return self.omo(["host", "session", *args, "--json"], timeout)[0]

    def available(self) -> tuple[bool, str]:
        if not self.bin.exists():
            return False, f"no pinned omo at {self.bin} (`lina omo install`)"
        answer, _ = self.omo(["daemon", "run", "--json"], 90)
        if not answer or answer.get("lifecycle") != "ready":
            return False, "omo daemon not ready"
        return True, str(self.bin)

    def honours(self, mode: str) -> tuple[bool, str]:
        return mode in ACCESS, "" if mode in ACCESS else f"unknown access {mode!r}"

    def _open_session(self, task: Task, prompt_file: Path, model: ModelProfile | None, run: int,
                      access: str) -> tuple[dict | None, int]:
        from hermes_cli.worker import proxy, settings
        from hermes_cli.worker.models import through_proxy
        err = task.path(f"stderr.{run}.txt")
        # omo reaches every model through this node's own magpie (lina#385, lina#585).
        provider, model_id = through_proxy(model, proxy.of(settings.section(self.root))) if model else (None, None)
        return self._call([str(self.node), str(HELPERS / "omo_open.mjs"), str(self.engine), str(task.work),
                           task.id, str(prompt_file), provider or "", model_id or "",
                           model.thinking or "" if model else "", access], 300, err)

    def start(self, task: Task, prompt: str, model: ModelProfile | None,
              access: str = "workspace-write") -> Handle:
        supported, reason = self.honours(access)
        if not supported:
            raise RunnerError(reason)
        prompt_file = task.path("prompt.1")
        if not prompt_file.exists():
            prompt_file.write_text(prompt, encoding="utf-8")
        ensure, _ = self.omo(["daemon", "run", "--json"], 120, task.path("stderr.1.txt"))
        task.path("ensure.1.json").write_text(json.dumps(ensure or {}), encoding="utf-8")
        opened, rc = self._open_session(task, prompt_file, model, 1, access)
        task.path("open.1.json").write_text(json.dumps(opened or {}), encoding="utf-8")
        if rc != 0 or not opened or not opened.get("durableSessionId"):
            raise RunnerError(f"omo started no session (rc {rc}): {(opened or {}).get('reason') or 'no answer'}")
        task.path("session.json").write_text(json.dumps(opened, ensure_ascii=False), encoding="utf-8")
        handle = Handle(task, 1, {"runner": self.name, "ref": opened["durableSessionId"],
                                  "model": model.as_dict() if model else None, "access": access})
        handle.save()
        return handle

    def _busy(self, ref: str) -> bool:
        state = (self.session(["state", ref], 60) or {}).get("state") or {}
        return bool(state.get("isStreaming")) if state else True

    def _restore_access(self, handle: Handle, access: str | None = None) -> str:
        mode = recorded_access(handle, access)
        reply, rc = self._call([str(self.node), str(HELPERS / "omo_open.mjs"), str(self.engine),
                                str(handle.task.work), handle.task.id, "", "", "", "", mode,
                                handle.data["ref"]], 120, handle.task.path(f"stderr.{handle.run}.txt"))
        if rc != 0 or not reply or reply.get("action") != "attach":
            reason = (reply or {}).get("detail") or (reply or {}).get("reason") or "no answer"
            raise RunnerError(f"omo could not restore recorded session access: {reason}")
        return mode

    def follow_up(self, handle: Handle, text: str, *, run: int | None = None,
                  access: str | None = None) -> tuple[str, Handle]:
        """Into the live run (a steer; an idle session between background wakes takes a prompt), or as run *run*."""
        recorded_access(handle, access)
        if run is None and self.permission_pending(handle):
            raise RunnerError("permission_dialog: permissions are decided by the run access mode")
        mode = self._restore_access(handle, access)
        ref, task = handle.data["ref"], handle.task
        stamp = run or handle.run
        text_file = task.path(f"prompt.{run}" if run else f"steer.{stamp}.{len(list(task.dir.glob('steer.*')))}")
        if not text_file.exists():
            text_file.write_text(text, encoding="utf-8")
        if run is not None:
            answer, rc = self.omo(["host", "session", "prompt", ref, f"@{text_file}", "--json"], 300,
                                  task.path(f"stderr.{run}.txt"))
            task.path(f"open.{run}.json").write_text(json.dumps(answer or {}), encoding="utf-8")
            if rc != 0:
                raise RunnerError(f"omo prompt failed (rc {rc})")
            new = Handle(task, run, {**handle.data, "access": mode})
            new.save()
            return "started", new
        answer = None
        if not self._busy(ref):
            answer = self.session(["prompt", ref, f"@{text_file}"], 120)
        if not answer or answer.get("action") != "prompt":
            answer = self.session(["steer", ref, f"@{text_file}"], 120)
        if not answer or answer.get("action") == "error":
            raise RunnerError(f"omo steer failed: {(answer or {}).get('reason') or 'no answer'}")
        handle.data["access"] = mode
        handle.save()
        return "steered", handle

    def answer(self, handle: Handle, text: str) -> dict:
        """Answer over the daemon socket; when no answer path takes it, close the run on its question so the answer
        goes to the next run (``answer_abort.<run>``)."""
        if self.permission_pending(handle):
            raise RunnerError("permission_dialog: permissions are decided by the run access mode")
        self._restore_access(handle)
        task = handle.task
        answer_file = task.path(f"answer.{handle.run}.{len(list(task.dir.glob('answer.*')))}")
        answer_file.write_text(text, encoding="utf-8")
        reply, _ = self._call([str(self.node), str(HELPERS / "omo_answer.mjs"), str(self.engine), handle.data["ref"],
                               str(answer_file), recorded_access(handle)], 120, task.path(f"stderr.{handle.run}.txt"))
        reply = reply or {"action": "error", "reason": "unreadable"}
        if reply.get("reason") == "permission_dialog":
            raise RunnerError("permission_dialog: permissions are decided by the run access mode")
        task.event(handle.run, "answer", reply={k: reply.get(k) for k in ("action", "steps", "still_pending",
                                                                           "reason")})
        if reply.get("action") == "answer" and reply.get("steps") and not reply.get("still_pending"):
            return reply
        task.path(f"answer_abort.{handle.run}").write_text(text, encoding="utf-8")
        self.session(["abort", handle.data["ref"]], 60)
        self.session(["wait", handle.data["ref"], "--until", "idle", "--timeout", "60000"], 90)
        return {**reply, "action": "aborted", "next_run": True}

    def _permissions(self, handle: Handle, action: str) -> dict:
        reply, rc = self._call([str(self.node), str(HELPERS / "omo_permissions.mjs"), str(self.engine),
                               handle.data["ref"], str(handle.task.work), recorded_access(handle), action],
                              120, handle.task.path(f"stderr.{handle.run}.txt"))
        if rc != 0 or not reply or reply.get("action") != "permissions":
            raise RunnerError(f"omo permission state unavailable: {(reply or {}).get('reason', 'no answer')}")
        return reply

    def permission_pending(self, handle: Handle) -> bool:
        return self._permissions(handle, "inspect")["pending"]

    def state(self, handle: Handle) -> str:
        snapshot = self.session(["state", handle.data["ref"]], 60)
        if not snapshot or snapshot.get("action") != "state":
            # An unreadable or error answer goes to Settle as it came (once), so its retry and error count apply.
            first = [snapshot]

            def as_read(args, timeout):
                return first.pop() if args[0] == "state" and first else self.session(args, timeout)

            return Settle(handle.task, handle.run, handle.data["ref"], as_read, self.limits).step()
        state = snapshot.get("state") or {}
        reply = self._permissions(handle, "settle") if state.get("pendingDialogs") else {
            "pending": False, "decisions": [], "state": state}
        for decision in reply["decisions"]:
            handle.task.event(handle.run, "permission_denied" if decision["value"] == "Deny" else "permission_allowed",
                              **{key: decision[key] for key in ("tool", "target", "mode", "request")})
        if reply["decisions"]:
            handle.task.path(f"permission_answered.{handle.run}").touch()
        if reply["pending"]:
            return "running"

        def call(args, timeout):
            # Judge the very snapshot whose dialogs were checked, not a second racy read.
            nonlocal snapshot
            if args[0] == "state" and snapshot is not None:
                answer, snapshot = {"action": "state", "state": reply["state"]}, None
                return answer
            return self.session(args, timeout)

        return Settle(handle.task, handle.run, handle.data["ref"], call, self.limits).step()

    def live_view(self, handle: Handle) -> dict:
        """What the session does now (``lina worker status``): read-only, never a settle step."""
        view = lifecycle_view(self.session(["state", handle.data["ref"]], 60))
        if view is None:
            return {}
        key, why = classify(view, {}, time.time(), float(self.limits.get("background_max_s", 3 * 3600.0)))
        tail = self.session(["read", handle.data["ref"], "--tail", "40"], 60) or {}
        tools, last, steps = [], "", []
        for entry in tail.get("entries") or []:
            message, at = entry.get("message") or {}, parse_time(entry.get("timestamp"))
            for part in message.get("content") or []:
                if isinstance(part, dict) and part.get("type") == "toolCall":
                    tools.append(part.get("name"))
                    steps.append({"at": at, "kind": "tool"})  # the run record (lina#201) shows a count, never a name
                if isinstance(part, dict) and part.get("type") == "text" and message.get("role") == "assistant":
                    text = (part.get("text") or "").strip()
                    last = text or last
                    if text:
                        steps.append({"at": at, "kind": "said", "text": text[:300]})
        return {"dash": key, "why": why, "lifecycle": view.get("state"), "streaming": view.get("isStreaming"),
                "pending": view.get("pendingMessageCount"), "questions": view.get("pendingQuestions"),
                "tools": tools[-6:], "last": last[:300], "tail": steps}

    def result(self, handle: Handle) -> dict[str, Path]:
        reply = self.session(["read", handle.data["ref"], "--messages"], 120) or {}
        final = ""
        for message in reply.get("messages") or []:
            content = message.get("content")
            if message.get("role") == "assistant" and isinstance(content, list):
                text = "".join(p.get("text", "") for p in content if isinstance(p, dict) and p.get("type") == "text")
                final = text.strip() or final
        handle.task.path("final.md").write_text(final + "\n", encoding="utf-8")
        return standard_result(handle.task)

    def stop(self, handle: Handle) -> None:
        if handle.data.get("ref"):
            if not stop_session(self.session, handle.data["ref"], STOP_WAIT_S):
                raise RunnerError("omo session did not stop")
            # Capture and reap descendants before unload can kill their terminal leader.
            handle.task.event(handle.run, "cancel_reap", **stop_task_processes(handle.task))
            self.session(["unload", handle.data["ref"], "--force"], STOP_WAIT_S)
        receipt = stop_task_processes(handle.task)
        handle.task.event(handle.run, "cancel_reap", **receipt)
        if receipt.get("left") or receipt.get("manifest_error") or receipt.get("errors"):
            raise RunnerError("omo task cleanup incomplete; see cancel_reap receipt")
