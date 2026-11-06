"""The ``command`` runner: any coding-agent CLI that reads its prompt on stdin, registered from config only.

```yaml
worker:
  runners:
    claude:
      type: command
      argv: [claude, -p, --permission-mode, acceptEdits]
      follow_up_argv: [claude, -p, --continue, --permission-mode, acceptEdits]   # optional: same session
      model_args: [--model, "{id}"]       # optional: added when the run carries a model profile
```

The CLI runs in the task's ``work/`` with the prompt on stdin (``{prompt_file}``, ``{work}`` and ``{id}`` are
filled in the argv for CLIs that want them as arguments). Its exit and ``report.md`` are the result: exit 0 is
``done``, anything else ``failed``; nothing written to stdout, stderr, ``report.md`` or ``out/`` for the stall
grace is ``stalled``. A CLI that needs the owner writes ``question.md`` and exits 0: the run waits on a person
(``waiting_human``) and ``answer`` continues it with ``follow_up_argv`` and the answer on stdin. A follow-up while
the CLI runs is refused: a CLI has no channel into a running turn.

Every launch goes through ``/bin/sh`` writing the exit code to ``exit.<run>.<step>``, so any process (``send``,
``cancel``, a restarted service) reads the same state from the task folder.
An answer saves the next step's handle before removing the question. State observes the question before loading
that handle, so an absent question cannot be combined with the previous step. It checks the wrapper's liveness
before reading that step's exit code: the wrapper publishes its exit code before exiting, not the other way round.
"""

from __future__ import annotations

import os
import signal
import subprocess
import time
from pathlib import Path

from hermes_platform.resolver import locate_command
from tools.environments.local import build_subprocess_env
from hermes_cli.worker.runners.base import Handle, ModelProfile, RunnerError, Task, standard_result
from hermes_cli.worker.runners.access import recorded_access
from hermes_cli.lina_work_settings import ACCESS

_WRAP = 'rc_file=$1; shift; "$@"; echo $? > "$rc_file"'
FINAL_MAX = 20000


def _group_alive(pgid: int | None) -> bool:
    """Any process left in the run's process group (the wrapper shell leads it; ``start_new_session``): the CLI under
    it can outlive the shell, e.g. when it ignores SIGTERM, so the shell's exit proves nothing about the run."""
    if not pgid:
        return False
    if _alive(pgid):  # reaps the leader once it exited, so a zombie leader is not counted
        return True
    try:
        os.killpg(pgid, 0)  # windows-footgun: ok — POSIX work devices only
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def _alive(pid: int | None) -> bool:
    if not pid:
        return False
    try:
        os.kill(pid, 0)  # windows-footgun: ok — POSIX work devices only
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    try:
        waited, _ = os.waitpid(pid, os.WNOHANG)
    except ChildProcessError:
        return True
    return waited == 0


class CommandRunner:
    def __init__(self, name: str, spec: dict, limits: dict | None = None):
        argv = spec.get("argv")
        if not isinstance(argv, list) or not argv or not all(isinstance(each, str) for each in argv):
            raise RunnerError(f"runner {name}: `argv` must be a list of strings")
        self.name, self.argv = name, list(argv)
        self.follow_up_argv = list(spec.get("follow_up_argv") or argv)
        self.model_args = list(spec.get("model_args") or [])
        self.access = spec.get("access")
        self.access_args = spec.get("access_args")
        if self.access_args is not None and (
            not isinstance(self.access_args, dict) or any(
                mode not in ACCESS or not isinstance(args, list) or not all(isinstance(arg, str) for arg in args)
                for mode, args in self.access_args.items()
            )
        ):
            raise RunnerError(f"runner {name}: `access_args` must map access modes to lists of strings")
        self.env = {str(k): str(v) for k, v in (spec.get("env") or {}).items()}
        self.limits = limits or {}

    def honours(self, mode: str) -> tuple[bool, str]:
        supported = mode in ACCESS and (mode in self.access_args if self.access_args is not None
                                       else mode == self.access)
        return supported, "" if supported else f"runner {self.name} cannot honour access {mode!r}"

    def _child_env(self) -> dict[str, str]:
        """The configured CLI signs in with its own credentials from the environment (API keys) and reads its own
        config under the person's real HOME, so neither is scrubbed or re-pointed; ``env`` from config goes last."""
        return build_subprocess_env(scrub_secrets=False, inherit_profile_home=False, extra=self.env)

    def available(self) -> tuple[bool, str]:
        found = locate_command(self.argv[0]).command
        return (True, found[0]) if found else (False, f"{self.argv[0]} not found on PATH")

    def _stall_s(self) -> float:
        return float(self.limits.get("stall_grace_s", 600.0))

    def _argv(self, base: list[str], task: Task, prompt_file: Path, model: ModelProfile | None) -> list[str]:
        values = {"prompt_file": str(prompt_file), "work": str(task.work), "id": task.id}
        if model is not None and model.id:
            values.update(provider=model.provider or "", id=model.id, thinking=model.thinking or "")
            base = base + self.model_args
        return [each.format(**values) for each in base]

    def _launch(self, task: Task, run: int, step: int, base: list[str], model: ModelProfile | None,
                text: str, access: str) -> Handle:
        supported, reason = self.honours(access)
        if not supported:
            raise RunnerError(reason)
        prompt_file = task.path(f"input.{run}.{step}.txt")
        prompt_file.write_text(text, encoding="utf-8")
        argv = self._argv(base, task, prompt_file, model)
        argv += self.access_args[access] if self.access_args is not None else []
        rc_file = task.path(f"exit.{run}.{step}")
        rc_file.unlink(missing_ok=True)
        with prompt_file.open("rb") as stdin, task.path(f"stdout.{run}.txt").open("ab") as out, \
                task.path(f"stderr.{run}.txt").open("ab") as err:
            # detached-spawn: ok — its own group so `stop` ends the whole CLI tree, and a service restart does not
            proc = subprocess.Popen(["/bin/sh", "-c", _WRAP, "sh", str(rc_file), *argv], start_new_session=True,
                                    cwd=task.work, stdin=stdin, stdout=out, stderr=err, env=self._child_env())
        task.event(run, "launched", step=step, argv=argv[:1])
        handle = Handle(task, run, {"runner": self.name, "pid": proc.pid, "step": step, "since": time.time(),
                                    "model": model.as_dict() if model else None, "access": access})
        handle.save()
        return handle

    def start(self, task: Task, prompt: str, model: ModelProfile | None,
              access: str = "workspace-write") -> Handle:
        return self._launch(task, 1, 1, self.argv, model, prompt, access)

    def follow_up(self, handle: Handle, text: str, *, run: int | None = None,
                  access: str | None = None) -> tuple[str, Handle]:
        mode = recorded_access(handle, access)
        if _alive(handle.data.get("pid")):
            return "refused", handle
        model = ModelProfile.from_dict(handle.data.get("model"))
        return "started", self._launch(handle.task, run or handle.run + 1, 1, self.follow_up_argv, model, text, mode)

    def _question(self, task: Task) -> Path:
        return task.work / "question.md"

    def _exit_code(self, handle: Handle) -> int | None:
        try:
            return int(handle.task.path(f"exit.{handle.run}.{handle.data.get('step', 1)}").read_text(encoding="utf-8-sig").strip())
        except (OSError, ValueError):
            return None

    def _quiet_since(self, handle: Handle) -> float:
        task, newest = handle.task, float(handle.data.get("since") or 0)
        watched = [task.path(f"stdout.{handle.run}.txt"), task.path(f"stderr.{handle.run}.txt"),
                   task.work / "report.md", self._question(task)]
        out = task.work / "out"
        if out.is_dir():
            watched += [p for p in out.rglob("*") if p.is_file()]
        for path in watched:
            try:
                newest = max(newest, path.stat().st_mtime)
            except OSError:
                continue
        return newest

    def state(self, handle: Handle) -> str:
        question = self._question(handle.task).exists()
        handle.data.update(Handle.load(handle.task, handle.run).data)  # an answer may have started the next step
        if handle.task.path("cancelled").exists():
            self.stop(handle)
            return "cancelled"
        alive = _alive(handle.data.get("pid"))
        code = self._exit_code(handle)
        quiet = time.time() - self._quiet_since(handle)
        if code is None and alive:
            if quiet >= self._stall_s():
                self.stop(handle)
                return "stalled"
            return "running"
        if code != 0:
            return "failed"
        # Seen before the handle so an answer's next step is never paired with "no question"; seen again after the
        # exit so a question the CLI wrote just before exiting is not missed (lina#199).
        if question or self._question(handle.task).exists():
            return "stalled" if quiet >= self._stall_s() else "waiting_human"
        return "done"

    def answer(self, handle: Handle, text: str) -> dict:
        access = recorded_access(handle)
        question = self._question(handle.task)
        if self._exit_code(handle) != 0 or not question.exists():
            return {"action": "refuse", "reason": "no_question"}
        step = int(handle.data.get("step", 1)) + 1
        model = ModelProfile.from_dict(handle.data.get("model"))
        self._launch(handle.task, handle.run, step, self.follow_up_argv, model,
                     f"Answer to your question in question.md: {text}", access)
        question.rename(handle.task.path(f"question.{handle.run}.{step - 1}.md"))  # after the step is recorded
        return {"action": "answer", "steps": 1, "still_pending": 0}

    def result(self, handle: Handle) -> dict[str, Path]:
        stdout = handle.task.path(f"stdout.{handle.run}.txt")
        if stdout.exists():
            text = stdout.read_text(encoding="utf-8-sig", errors="replace").strip()
            handle.task.path("final.md").write_text(text[-FINAL_MAX:] + "\n", encoding="utf-8")
        return standard_result(handle.task)

    def stop(self, handle: Handle) -> None:
        pid = handle.data.get("pid")
        if not _group_alive(pid):
            return
        for sig in (signal.SIGTERM, signal.SIGKILL):  # windows-footgun: ok — POSIX work devices only
            try:
                os.killpg(pid, sig)  # windows-footgun: ok — POSIX work devices only
            except (ProcessLookupError, PermissionError):
                return
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline and _group_alive(pid):
                time.sleep(0.05)
            if not _group_alive(pid):
                return
