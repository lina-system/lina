"""The device side of ``lina worker``: the ``_device`` verbs the hub calls, preparing a task, and one run.

A run is one process (``lina worker _run <ID> <n>``), started by ``lina worker serve`` in its own transient unit
(systemd, with a memory cap) or session (``setsid``), so a service restart never takes a running task down. It starts
the runner (run 1) or hands it the follow-up (run n), polls the runner's state until the run ended, writes
``final.md``, ``git.txt`` and ``manifest.json``, decides the ending state and reports it to the hub.
"""

from __future__ import annotations

import contextlib
import fcntl  # windows-footgun: ok — work devices are POSIX (launchd/systemd) only
import io
import json
import os
import shutil
import subprocess
import sys
import tarfile
import time
from pathlib import Path

import psutil

from hermes_cli.process_identity import process_birth
from hermes_cli.worker import runners, settings, slots
from hermes_platform.resolver import locate_command
from tools.environments.local import build_subprocess_env
from hermes_cli.worker.runners.base import LIVE, Handle, ModelProfile, RunnerError, Task
from hermes_cli.worker.task import (end_state, git_summary, lock_inputs, read_json, safe_id, stamp, write_json,
                                    write_manifest)
from hermes_cli.worker.transport import notify_hub

LIMIT_KEYS = ("poll_s", "stall_grace_s", "answered_grace_s", "background_max_s", "hard_timeout_s")
HEARTBEAT = "serve.heartbeat"


def base_of(worker_root: Path) -> Path:
    return worker_root.parent


def task_of(worker_root: Path, task_id: str) -> Task:
    worker = settings.section(base_of(worker_root))
    limits = {key: float(settings.setting(key, worker=worker)) for key in LIMIT_KEYS}
    return Task(safe_id(task_id), settings.tasks_dir(worker_root) / task_id, limits)


def job_of(task: Task) -> dict:
    return read_json(task.path("job.json"), {}) or {}


def runner_for(worker_root: Path, task: Task, job: dict):
    """The job's runner: this device's own entry of that name when it has one (its local paths), else the hub's."""
    worker = settings.section(base_of(worker_root))
    name = job.get("runner") or runners.default_name(worker)
    configured = worker.get("runners") if isinstance(worker.get("runners"), dict) else {}
    spec = configured.get(name) or job.get("runner_spec") or runners.specs(worker).get(name) or {"type": name}
    return runners.build(name, spec, task.limits, base_of(worker_root))


def last_run(task: Task) -> int:
    numbers = [int(p.name.split(".")[1]) for p in task.dir.glob("prompt.*") if p.name.split(".")[1].isdigit()]
    return max(numbers, default=0)


_CHILDREN: list[subprocess.Popen] = []  # run processes this serve started without systemd, reaped by reap()


def reap() -> None:
    """Collect the exit status of run processes that ended, so none stays a zombie of the service."""
    _CHILDREN[:] = [proc for proc in _CHILDREN if proc.poll() is None]


def _process_alive(record: dict) -> bool:
    """The recorded run process is still that process (same PID and birth), and has not exited."""
    try:
        proc = psutil.Process(int(record["pid"]))
        if proc.status() == psutil.STATUS_ZOMBIE:
            return False
        return abs(process_birth(proc) - float(record["birth"])) < 2.0
    except (psutil.Error, KeyError, TypeError, ValueError):
        return False


def alive(task: Task, run: int) -> bool:
    unit = task.path(f"unit.{run}")
    if unit.exists():
        done = subprocess.run(["systemctl", "--user", "is-active", "--quiet",
                               unit.read_text(encoding="utf-8-sig").strip()], capture_output=True, timeout=10, check=False)
        return done.returncode == 0
    record = read_json(task.path(f"pid.{run}"))
    return isinstance(record, dict) and _process_alive(record)


def _read(task: Task, name: str) -> str:
    try:
        return task.path(name).read_text(encoding="utf-8-sig", errors="replace").strip()
    except OSError:
        return ""


def _git_env() -> dict[str, str]:
    """The device's own git for the repo cache and worktrees: its credentials (helpers, tokens for a private clone)
    and the person's real HOME (``~/.gitconfig``) as the service sees them, and never a prompt."""
    return build_subprocess_env(scrub_secrets=False, inherit_profile_home=False, extra={"GIT_TERMINAL_PROMPT": "0"})


def _git(*args: str, cwd: Path | None = None, timeout: float = 60) -> subprocess.CompletedProcess:
    return subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, encoding="utf-8", errors="replace",
                          timeout=timeout, check=False, env=_git_env())


def _run_git(*args: str, cwd: Path | None = None) -> None:
    done = _git(*args, cwd=cwd, timeout=900)
    if done.returncode != 0:
        raise RunnerError(f"git {args[0]}: {(done.stderr or done.stdout).strip()[-300:]}")


def _repo_source(worker_root: Path, repo: str) -> Path:
    """A path on the device is used as it is; ``owner/name`` is cloned once into the repo cache and fetched."""
    if repo.startswith(("/", "~")):
        return Path(repo).expanduser()
    cache = settings.repos_dir(worker_root) / repo.replace("/", "__")
    if (cache / ".git").exists():
        _git("-C", str(cache), "fetch", "-q", "origin", timeout=900)
        return cache
    cache.parent.mkdir(parents=True, exist_ok=True)
    url = str(settings.setting("repo_url", base_of(worker_root))).format(repo=repo)
    _run_git("clone", "-q", url, str(cache))
    identity = settings.section(base_of(worker_root)).get("git_identity") or {}
    for key in ("name", "email"):
        if identity.get(key):
            _run_git("-C", str(cache), "config", f"user.{key}", str(identity[key]))
    return cache


def _prepare_repo(worker_root: Path, task: Task, job: dict) -> None:
    source = _repo_source(worker_root, job["repo"])
    _git("-C", str(source), "fetch", "-q", "origin", timeout=900)
    base = job.get("base_branch") or _git("-C", str(source), "symbolic-ref", "-q", "--short",
                                          "refs/remotes/origin/HEAD").stdout.strip() or "HEAD"
    _run_git("-C", str(source), "worktree", "add", "-q", "-b", job["branch"], str(task.work), base)
    # The block lives in this worktree's own config: in the shared config it would also block the person's
    # checkout and every sibling worktree, and stay after the task.
    _run_git("-C", str(source), "config", "extensions.worktreeConfig", "true")
    _run_git("-C", str(task.work), "config", "--worktree", "remote.origin.pushurl", "PUSH-BLOCKED-by-lina-worker")
    exclude = _git("-C", str(task.work), "rev-parse", "--git-path", "info/exclude").stdout.strip()
    exclude_path = Path(exclude) if os.path.isabs(exclude) else task.work / exclude
    exclude_path.parent.mkdir(parents=True, exist_ok=True)
    with exclude_path.open("a", encoding="utf-8") as fh:
        fh.write("report.md\nout/\ninput/\nquestion.md\n")
    head = _git("-C", str(task.work), "rev-parse", "HEAD").stdout.strip()
    task.path("base").write_text(head + "\n", encoding="utf-8")


def prepare(worker_root: Path, task: Task) -> None:
    """The task's ``work/``: a fresh worktree on ``omo/<ID>`` with push blocked, or a plain folder; then the inputs
    the hub copied in, made read-only and hashed."""
    job = job_of(task)
    if job.get("repo"):
        _prepare_repo(worker_root, task, job)
    task.work.mkdir(parents=True, exist_ok=True)
    (task.work / "out").mkdir(exist_ok=True)
    upload = task.path("input.upload")
    if upload.is_dir():
        shutil.move(str(upload), str(task.work / "input"))
        lock_inputs(task)
    stamp(task, "prepared")


def _start_or_follow(runner, task: Task, run: int, job: dict) -> Handle:
    prompt = task.path(f"prompt.{run}").read_text(encoding="utf-8-sig")
    if run == 1:
        return runner.start(task, prompt, ModelProfile.from_dict(job.get("model")))
    return runner.follow_up(Handle.load(task), prompt, run=run)[1]


def _settle(runner, task: Task, run: int, handle: Handle) -> str:
    while True:
        state = runner.state(handle)
        write_json(task.path(f"live.{run}.json"), {"state": state, "at": time.time()})
        if state not in LIVE:
            return state
        time.sleep(task.limits.get("poll_s", 15.0))


def run_task(worker_root: Path, task_id: str, run: int) -> str:
    """``lina worker _run``: the whole of run *run*, then the end report. Returns the ending state."""
    task = task_of(worker_root, task_id)
    job = job_of(task)
    task.path("state").write_text("running\n", encoding="utf-8")
    stamp(task, f"started.{run}")
    task.event(run, "started", runner=job.get("runner"))
    runner, handle, code = None, None, 0
    if task.path("cancelled").exists():  # cancelled between the launch and here: no session is opened
        task.path(f"rc.{run}").write_text("0\n", encoding="utf-8")
        stamp(task, f"ended.{run}")
        return finish_run(worker_root, task, run, "cancelled")
    try:
        runner = runner_for(worker_root, task, job)
        handle = _start_or_follow(runner, task, run, job)
        stamp(task, f"accepted.{run}")
        runner_state = _settle(runner, task, run, handle)
    except (RunnerError, OSError) as exc:
        runner_state, code = "failed", 1
        with task.path(f"stderr.{run}.txt").open("a", encoding="utf-8") as fh:
            fh.write(f"lina worker: {exc}\n")
        if not task.path(f"wait.{run}.json").exists():
            write_json(task.path(f"wait.{run}.json"), {"outcome": "start_failed", "why": str(exc)[:300]})
        stop_runner(runner, handle)
    task.path(f"rc.{run}").write_text(f"{code}\n", encoding="utf-8")
    stamp(task, f"ended.{run}")
    return finish_run(worker_root, task, run, runner_state, runner, handle)


def stop_runner(runner, handle: Handle | None) -> None:
    """A run the worker ends on its own (an error, a dead run process) stops its agent before the slot is freed."""
    if runner is None or handle is None:
        return
    try:
        runner.stop(handle)
    except RunnerError as exc:
        with handle.task.path(f"stderr.{handle.run}.txt").open("a", encoding="utf-8") as fh:
            fh.write(f"lina worker: stopping the runner failed: {exc}\n")


def finish_run(worker_root: Path, task: Task, run: int, runner_state: str, runner=None, handle=None) -> str:
    files = runner.result(handle) if runner is not None and handle is not None else None
    summary = git_summary(task)
    if summary:
        task.path("git.txt").write_text(summary, encoding="utf-8")
    manifest = write_manifest(task, run, files)
    job = job_of(task)
    final = end_state(runner_state, manifest, cancelled=task.path("cancelled").exists(),
                      next_notes_required=bool(job.get("next_notes_rule", True)))
    task.path("state").write_text(final + "\n", encoding="utf-8")
    task.event(run, "ended", state=final, runner_state=runner_state)
    if not task.path(f"answer_abort.{run}").exists():  # closed by `send`: the hub records that end itself, silently
        report_end(worker_root, task, final, run)
    return final


def report_end(worker_root: Path, task: Task, state: str, run: int) -> None:
    pending = task.path(f"notify_pending.{run}")
    if notify_hub(job_of(task).get("notify") or {}, task.id, state, run, worker_root):
        pending.unlink(missing_ok=True)
    else:
        pending.write_text(state + "\n", encoding="utf-8")


def _put(worker_root: Path, task_id: str, rel: str, data: bytes) -> dict:
    task_dir = settings.tasks_dir(worker_root) / safe_id(task_id)
    target = (task_dir / rel).resolve()
    if task_dir.resolve() not in target.parents:
        return {"action": "error", "reason": "outside_task"}
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(data)
    return {"action": "put", "bytes": len(data)}


def _untar(worker_root: Path, task_id: str, rel: str, data: bytes) -> dict:
    task_dir = settings.tasks_dir(worker_root) / safe_id(task_id)
    target = (task_dir / rel).resolve()
    if task_dir.resolve() not in target.parents:
        return {"action": "error", "reason": "outside_task"}
    target.mkdir(parents=True, exist_ok=True)
    with tarfile.open(fileobj=io.BytesIO(data), mode="r") as archive:
        archive.extractall(target, filter="data")
        return {"action": "untar", "files": len(archive.getnames())}


def meta(worker_root: Path, task_id: str, live: bool = False) -> dict:
    task = task_of(worker_root, task_id)
    if not task.dir.is_dir():
        return {"missing": True}
    run = last_run(task)
    state = _read(task, "state")
    if state in ("done", "done_empty", "review", "stalled", "failed") and run and not task.path(f"rc.{run}").exists():
        state = "running"  # a follow-up run whose process has not started yet
    is_alive = alive(task, run)
    view = read_json(task.path(f"lifecycle.{run}.json"), {}) or {}
    live_view = {"dash": view.get("dash"), "why": view.get("why"), "lifecycle": view.get("state"),
                 "last_at": view.get("last_entry_at")} if view else {}
    contract = read_json(task.path(f"live.{run}.json"), {}) or {}
    if contract.get("state") == "waiting_human" and not live_view.get("dash"):
        live_view["dash"] = "human"
    if live and is_alive and task.path("handle.json").exists():
        runner = runner_for(worker_root, task, job_of(task))
        if hasattr(runner, "live_view"):
            live_view.update({k: v for k, v in runner.live_view(Handle.load(task)).items() if v is not None})
    return {"state": state or "new", "run": run, "requested": slots.requested(task.dir), "rc": _read(task, f"rc.{run}"), "alive": is_alive,
            "report": (task.work / "report.md").is_file(), "final": _read(task, "final.md")[:600], "live": live_view}


def _free_gb(path: Path) -> int:
    path.mkdir(parents=True, exist_ok=True)
    return int(shutil.disk_usage(path).free / (1 << 30))


def probe(worker_root: Path, repo: str = "", runner_name: str = "") -> dict:
    """What the hub bids with: readiness of the runner, the service, running tasks, load, disk and the repo path."""
    worker = settings.section(base_of(worker_root))
    name = runner_name or runners.default_name(worker)
    try:
        ready, why = runners.get(name, worker, root=base_of(worker_root)).available()
    except RunnerError as exc:
        ready, why = False, str(exc)
    heartbeat = settings.tasks_dir(worker_root).parent / HEARTBEAT
    serve = heartbeat.exists() and time.time() - heartbeat.stat().st_mtime < 60
    running = slots.held(worker_root)
    repo_ok = not repo.startswith(("/", "~")) or (Path(repo).expanduser() / ".git").exists()
    load = os.getloadavg()[0] if hasattr(os, "getloadavg") else 0.0
    return {"runner": name, "ready": ready, **({"runner_at": why} if ready else {"why": why}), "serve": serve, "running": running,
            "cores": os.cpu_count() or 1, "load": round(load, 2), "free_gb": _free_gb(worker_root), "repo": repo_ok}


def steer(worker_root: Path, task_id: str, text: str) -> dict:
    task = task_of(worker_root, task_id)
    run = last_run(task)
    if not alive(task, run) or not task.path("handle.json").exists():
        return {"action": "not_live", "run": run}
    runner = runner_for(worker_root, task, job_of(task))
    try:
        how, _ = runner.follow_up(Handle.load(task, run), text)
    except RunnerError as exc:
        return {"action": "error", "reason": str(exc)[:300], "run": run}
    task.event(run, "follow_up", how=how)
    return {"action": how, "run": run}


@contextlib.contextmanager
def task_lock(task: Task):
    """One task's runs change hands under this lock: a follow-up asking for the next run and ``serve`` taking one."""
    task.dir.mkdir(parents=True, exist_ok=True)
    with task.path("runs.lock").open("w") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)  # windows-footgun: ok — POSIX work devices only
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)  # windows-footgun: ok — POSIX work devices only


def follow_up(worker_root: Path, task_id: str, payload: dict) -> dict:
    """A person's follow-up, so that a task has at most one requested-or-running run: queued into the newest run
    while it waits for ``serve``, steered into it while it is live, refused while it is starting; once that run
    ended, the next run is requested."""
    task = task_of(worker_root, task_id)
    with task_lock(task):
        run = slots.requested(task.dir)
        if run and not task.path(f"rc.{run}").exists():
            if not task.path(f"taken.{run}").exists():
                with task.path(f"prompt.{run}").open("a", encoding="utf-8") as fh:
                    fh.write("\n\n" + payload["message"])
                task.event(run, "follow_up", how="queued")
                return {"action": "queued", "run": run}
            reply = steer(worker_root, task_id, payload["message"])
            return reply if reply["action"] != "not_live" else {"action": "busy", "run": run}
        run = max(run, last_run(task)) + 1
        task.path(f"prompt.{run}").write_text(payload["prompt"], encoding="utf-8")
        task.path(f"request.{run}").write_text(json.dumps({"run": run, "at": time.time()}), encoding="utf-8")
        return {"action": "requested", "run": run}


def human(worker_root: Path, task_id: str) -> dict:
    task = task_of(worker_root, task_id)
    run = last_run(task)
    contract = read_json(task.path(f"live.{run}.json"), {}) or {}
    return {"open": alive(task, run) and contract.get("state") == "waiting_human", "run": run}


def answer(worker_root: Path, task_id: str, text: str) -> dict:
    task = task_of(worker_root, task_id)
    run = last_run(task)
    runner = runner_for(worker_root, task, job_of(task))
    try:
        reply = runner.answer(Handle.load(task, run), text)
    except RunnerError as exc:
        reply = {"action": "error", "reason": str(exc)[:300]}
    task.event(run, "answer", action=reply.get("action"))
    return {**reply, "run": run}


def cancel(worker_root: Path, task_id: str, why: str) -> dict:
    task = task_of(worker_root, task_id)
    if not task.dir.is_dir():
        return {"action": "cancel", "missing": True}
    run = last_run(task)
    task.path("cancelled").write_text((why or "cancelled") + "\n", encoding="utf-8")
    if task.path("handle.json").exists():
        try:
            runner_for(worker_root, task, job_of(task)).stop(Handle.load(task, run))
        except RunnerError:
            pass  # nothing to stop: the run's process sees the cancel file on its next poll
    is_alive = alive(task, run)
    if not is_alive:
        task.path("state").write_text("cancelled\n", encoding="utf-8")
    task.event(run, "cancel", why=why[:300])
    return {"action": "cancel", "run": run, "alive": is_alive}


def pack(worker_root: Path, task_id: str) -> bytes:
    task = task_of(worker_root, task_id)
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w") as archive:
        for source, name in ((task.work / "report.md", "report.md"), (task.path("final.md"), "final.md"),
                             (task.path("manifest.json"), "manifest.json"), (task.path("git.txt"), "git.txt"),
                             (task.path(f"stderr.{last_run(task)}.txt"), "stderr.txt"), (task.work / "out", "out")):
            if source.exists():
                archive.add(str(source), arcname=name)
    return buffer.getvalue()


def _ok(value: dict) -> tuple[int, bytes]:
    code = 1 if value.get("action") == "error" else 0
    return code, (json.dumps(value, ensure_ascii=False) + "\n").encode("utf-8")


def dispatch(worker_root: Path, verb: list[str], stdin: bytes = b"") -> tuple[int, bytes]:
    """One ``_device`` verb: ``(exit code, stdout bytes)``."""
    name, args = verb[0], verb[1:]
    text = stdin.decode("utf-8", "replace")
    try:
        table = {
            "put": lambda: _ok(_put(worker_root, args[0], args[1], stdin)),
            "untar": lambda: _ok(_untar(worker_root, args[0], args[1], stdin)),
            "meta": lambda: _ok(meta(worker_root, args[0], live=args[1:] == ["live"])),
            "probe": lambda: _ok(probe(worker_root, *args[:2])),
            "reserve": lambda: _ok(slots.reserve(worker_root, safe_id(args[0]), int(args[1]))),
            "send": lambda: _ok(steer(worker_root, args[0], text)),
            "follow_up": lambda: _ok(follow_up(worker_root, args[0], json.loads(text))),
            "human": lambda: _ok(human(worker_root, args[0])),
            "answer": lambda: _ok(answer(worker_root, args[0], text)),
            "cancel": lambda: _ok(cancel(worker_root, args[0], text)),
            "pack": lambda: (0, pack(worker_root, args[0])),
        }
        if name not in table:
            return _ok({"action": "error", "reason": f"unknown verb {name}"})
        return table[name]()
    except (IndexError, ValueError, OSError) as exc:
        return _ok({"action": "error", "reason": f"{name}: {exc}"[:300]})


def run_argv(task: Task, run: int) -> list[str]:
    return [sys.executable, "-m", "hermes_cli.main", "worker", "_run", task.id, str(run)]


def launch(worker_root: Path, task: Task, run: int) -> None:
    """Start run *run* as its own process: a transient systemd user unit with the memory cap where there is
    systemd, else a new session (``setsid``). Either way a restart of the service leaves it running."""
    argv = run_argv(task, run)
    # The run process is lina itself serving this LINA root: it keeps the service's HERMES_HOME (no profile bridge)
    # and the credentials the runner's agent needs, and finds hermes_cli through PYTHONPATH.
    child_env = build_subprocess_env(scrub_secrets=False, inherit_profile_home=False,
                                     extra={"PYTHONPATH": settings.child_pythonpath()})
    if sys.platform.startswith("linux") and locate_command("systemd-run").found:
        unit = f"lina-worker-{task.id}-{run}"
        env = [f"--setenv={key}={child_env[key]}" for key in ("HERMES_HOME", "PATH", "HOME", "LANG", "PYTHONPATH")
               if child_env.get(key)]
        cap = settings.setting("memory_max", base_of(worker_root))
        done = subprocess.run(["systemd-run", "--user", "--quiet", "--collect", "--unit", unit, "-p",
                               f"MemoryMax={cap}", *env, "--", *argv], capture_output=True, timeout=60, check=False)
        if done.returncode == 0:
            task.path(f"unit.{run}").write_text(unit + "\n", encoding="utf-8")
            return
    with task.path(f"runner.{run}.log").open("ab") as log:
        # detached-spawn: ok — a run must outlive the service that started it (restarts never end a task)
        proc = subprocess.Popen(argv, start_new_session=True, stdin=subprocess.DEVNULL, stdout=log,
                                stderr=subprocess.STDOUT, env=child_env)
    _CHILDREN.append(proc)
    try:
        birth = process_birth(psutil.Process(proc.pid))
    except psutil.Error:
        birth = None  # it already ended: alive() reads it as gone
    write_json(task.path(f"pid.{run}"), {"pid": proc.pid, "birth": birth})
