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
import urllib.error
import urllib.request
from pathlib import Path

import psutil

from hermes_cli.process_identity import process_birth
from hermes_cli.lina_work_settings import ACCESS
from hermes_cli.worker import proxy, runners, settings, slots
from hermes_platform.resolver import locate_command
from tools.environments.local import build_subprocess_env
from hermes_cli.worker.runners.base import LIVE, Handle, ModelProfile, RunnerError, Task, iter_artifact_files
from hermes_cli.worker.task import (end_state, git_summary, lock_inputs, read_json, safe_id, stamp, write_json,
                                    write_manifest)
from hermes_cli.worker.transport import PACKED, notify_hub

LIMIT_KEYS = ("poll_s", "stall_grace_s", "answered_grace_s", "background_max_s", "hard_timeout_s", "human_max_s")
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
    """The job's runner: this device's own entry of that name when it has one (its local paths), else the hub's.
    The model is always the job's (lina#257): the device's entry never names one, and when it has no ``model_args``
    the hub's are used, so the job's model reaches the CLI."""
    worker = settings.section(base_of(worker_root))
    name = job.get("runner") or runners.default_name(worker)
    spec = runners.spec_for(worker, name, job.get("runner_spec")) or {"type": name}
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
    base = job.get("base_branch")
    if base and _git("-C", str(source), "rev-parse", "-q", "--verify", f"refs/remotes/origin/{base}").returncode == 0:
        base = f"origin/{base}"  # the cache is only fetched: its local branch of that name can be behind origin's
    base = base or _git("-C", str(source), "symbolic-ref", "-q", "--short",
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
    supported, reason = runner_for(worker_root, task, job).honours(job.get("access", "workspace-write"))
    if not supported:
        raise RunnerError(reason)
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
    access = job.get("access", "workspace-write")
    supported, reason = runner.honours(access)
    if not supported:
        raise RunnerError(reason)
    prompt = task.path(f"prompt.{run}").read_text(encoding="utf-8-sig")
    if run == 1:
        return runner.start(task, prompt, ModelProfile.from_dict(job.get("model")), access=access)
    return runner.follow_up(Handle.load(task), prompt, run=run, access=access)[1]


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
    task.event(run, "started", runner=job.get("runner"), access=job.get("access", "workspace-write"))
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
    task.path(f"pid.{run}").unlink(missing_ok=True)
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
            "report": (task.work / "report.md").is_file(), "final": _read(task, "final.md")[:600], "live": live_view,
            "access": (read_json(task.path("job.json"), {}) or {}).get("access")}


def _free_gb(path: Path) -> int:
    path.mkdir(parents=True, exist_ok=True)
    return int(shutil.disk_usage(path).free / (1 << 30))


def hub_spec_of(data: bytes) -> dict | None:
    """The hub's runner spec a ``probe`` carries on stdin as JSON (lina#337); None when it sent none."""
    if not data.strip():
        return None
    spec = json.loads(data)
    if not isinstance(spec, dict):
        raise ValueError("the runner spec on stdin is not a JSON object")
    return spec


def needs_model_path(worker: dict, name: str, spec: dict | None) -> bool:
    """Whether runner *name* (built from *spec*) reaches its model through this node's model proxy (``worker.proxy``,
    ``ocx`` or ``magpie``), so the proxy has to answer before the node takes work: the omo runner, or a pinned runner
    that signs in through the proxy (pin sign-in ``ocx``: claude, codex and their ACP adapters, whichever kind the
    node runs). A command runner carrying its own credentials (grok, agy, cursor-agent) and a pin whose sign-in is
    unknown (dsh) needs no model path on either kind — the same exemption ``bidding.proxy_down()`` makes for a node
    that reports no proxy."""
    from hermes_cli import lina_runners
    if (spec or {}).get("type") == "omo":
        return True
    pairs = lina_runners.offered(worker)
    named = next((pin for offered, _kind, pin in pairs if offered == name and pin is not None), None)
    pin = named or lina_runners.pins().get(name)  # a runner the hub names that this node does not list itself
    return pin is not None and pin.sign_in == "ocx"


MAGPIE_CHECK = "http://127.0.0.1:3425/v1/models"  # a node's local magpie: it has no /healthz (404)


def node_proxy(worker_root: Path) -> proxy.Proxy:
    return proxy.of(settings.section(base_of(worker_root)))


def model_check_url(worker_root: Path, kind: proxy.Proxy | None = None) -> str:
    """The model path this node asks, by its proxy *kind* (the node's own ``worker.proxy`` when not given):
    ``worker.proxy.health`` when set; else for ``magpie`` its local ``/v1/models`` (``MAGPIE_CHECK``); else for
    ``ocx`` the ``model_check`` its config gives the local device it lists (the hub is a device of itself), else the
    same default on this root's own ``ocx.port`` — which is not always 10100. Asked here, never on the hub: a node's
    127.0.0.1 is the node's own."""
    kind = kind or node_proxy(worker_root)
    if kind.health:
        return kind.health
    if kind.kind == proxy.MAGPIE:
        return MAGPIE_CHECK
    base = base_of(worker_root)
    local = next((dev for dev in settings.devices(base) if dev.local), None)
    if local is not None and local.model_check:
        return local.model_check
    from hermes_cli import lina_ocx
    try:
        port = lina_ocx.configured_port(base)
    except (lina_ocx.OcxError, KeyError, ValueError, TypeError):
        port = lina_ocx.DEFAULT_PORT
    return f"http://127.0.0.1:{port}/healthz"


def http_status(url: str, timeout: float = 2.0) -> int:
    """The HTTP status *url* answers with; 0 when nothing answers (a refused connection, a timeout, a bad URL)."""
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response:
            return int(response.status)
    except urllib.error.HTTPError as exc:
        return int(exc.code)
    except (urllib.error.URLError, OSError, ValueError):
        return 0


def models_listed(url: str, timeout: float = 2.0, key: str | None = None) -> tuple[int, int]:
    """``(HTTP status, models in its ``data`` list)`` of an OpenAI-style ``/v1/models`` at *url*: status 0 when
    nothing answers, models 0 when the body is not such a list. Reads the list only; *key*, when given, goes as
    ``Authorization: Bearer`` and nowhere else."""
    try:
        request = urllib.request.Request(url, headers={"Authorization": f"Bearer {key}"} if key else {})
        with urllib.request.urlopen(request, timeout=timeout) as response:
            status, body = int(response.status), response.read()
    except urllib.error.HTTPError as exc:
        return int(exc.code), 0
    except (urllib.error.URLError, OSError, ValueError):
        return 0, 0
    try:
        data = json.loads(body).get("data")
    except (ValueError, AttributeError):
        return status, 0
    return status, len(data) if isinstance(data, list) else 0


def proxy_check(worker_root: Path) -> dict:
    """This node's proxy as its check finds it: ``{kind, url, health}`` (health: the HTTP status, None when nothing
    answers), and for ``magpie`` ``models``, the number of models its local ``/v1/models`` lists. ``ocx`` asks
    ``/healthz`` for 200 as before; ``magpie`` has no ``/healthz`` (404) and is up on 200 with at least one model.

    The limit: a local ``/v1/models`` 200 does not prove the node's link to the hub — the node's magpie lists the
    linked hub's models without calling the hub for each one. The real-call probe (``lina_runner_probe``, lina#531)
    stays the proof that a model call goes through. Nothing here calls the hub. A ``magpie`` node whose gateway
    answers only with a key sends the one ``worker.proxy.key_env`` names (lina#625); the key is in no field."""
    from hermes_cli.lina_magpie import gateway_key

    kind = node_proxy(worker_root)
    url = model_check_url(worker_root, kind)
    if kind.kind == proxy.MAGPIE:
        code, models = models_listed(url, key=gateway_key(kind))
        return {"kind": kind.kind, "url": url, "health": code or None, "models": models}
    return {"kind": kind.kind, "url": url, "health": http_status(url) or None}


def proxy_up(check: dict) -> bool:
    return check["health"] == 200 and (check["kind"] != proxy.MAGPIE or check["models"] > 0)


def proxy_down_why(check: dict) -> str:
    """The reason a node whose proxy check failed is passed. ``ocx`` keeps the reason it had before lina#586."""
    if check["kind"] == proxy.OCX:
        return f"model path does not answer ({check['url']}: HTTP {check['health'] or 'none'})"
    if check["health"] == 200:
        return f"model path lists no model ({check['kind']} {check['url']}: HTTP 200, 0 models)"
    return f"model path does not answer ({check['kind']} {check['url']}: HTTP {check['health'] or 'none'})"


def model_path(worker_root: Path, name: str = "", spec: dict | None = None) -> dict:
    """Whether this node's model path answers for the runner asked (its default runner when none was named):
    ``{"model_path": "ok"}``, else ``"down"`` with the reason a run there would end ``provider_error`` straight away.
    The path is the node's proxy (``proxy_check``). A runner that reaches models without it is asked for no model
    path, and the reply then carries no ``model_path`` key at all, so the node bids exactly as it did before this
    check."""
    worker = settings.section(base_of(worker_root))
    name = name or runners.default_name(worker)
    spec = spec if spec is not None else runners.specs(worker).get(name)
    if not needs_model_path(worker, name, spec):
        return {}
    check = proxy_check(worker_root)
    if proxy_up(check):
        return {"model_path": "ok"}
    return {"model_path": "down", "model_path_why": proxy_down_why(check)}


def proxy_report(worker_root: Path, ocx: dict) -> dict | None:
    """``hello``'s ``capabilities.proxy``: ``{kind, health}`` (health: the HTTP status of the node's proxy check, None
    when nothing answers), ``version`` where known and, for ``magpie``, ``models``, ``remote_id`` (its link to the hub)
    and ``service`` ``{name, state}`` of its local magpie service (lina#589). An ``ocx`` node reports the OCX
    its ``capabilities.ocx`` (*ocx*) names, with the same health and version; one that reports no OCX (``external``
    false and no ``version``) reports no proxy, so it is never passed for one. No key is read."""
    if node_proxy(worker_root).kind == proxy.OCX:
        if not (ocx.get("external") is True or ocx.get("version") is not None):
            return None
        return {"kind": proxy.OCX, "health": ocx.get("health"), "version": ocx.get("version")}
    from hermes_cli.worker import proxy_service
    check = proxy_check(worker_root)
    return {"kind": check["kind"], "health": check["health"], "models": check["models"],
            "remote_id": node_proxy(worker_root).remote_id, "service": proxy_service.state(worker_root)}


def probe_failed(base: Path, worker: dict, name: str) -> str | None:
    """Why runner *name* is not ready after its static checks passed: the last real-call probe of the version this
    node runs failed (``lina_runner_probe``, lina#531). None when it passed, was not run, or probed another version."""
    from hermes_cli import lina_runner_probe, lina_runners
    pin = next((pin for offered, _kind, pin in lina_runners.offered(worker) if offered == name and pin), None)
    if pin is None:
        return None
    found = lina_runner_probe.verdict(base, pin, lina_runners.current_version(base, pin))
    return f"runner {name}: last probe failed ({found.get('why')})" if found and found.get("state") == "failed" \
        else None


def probe(worker_root: Path, repo: str = "", runner_name: str = "", hub_spec: dict | None = None) -> dict:
    """What the hub bids with: readiness of the runner, the service, running tasks, load, disk, available memory, the
    repo path and whether the model path answers.
    The runner is built as the run builds it (``runner_for``): the node's own entry, else the hub's *hub_spec*."""
    worker = settings.section(base_of(worker_root))
    name = runner_name or runners.default_name(worker)
    spec = runners.spec_for(worker, name, hub_spec)
    try:
        if spec is None:
            raise RunnerError(f"no runner {name!r} (configured: {', '.join(runners.specs(worker))})")
        ready, why = runners.build(name, spec, root=base_of(worker_root)).available()
    except RunnerError as exc:
        ready, why = False, str(exc)
    failed = probe_failed(base_of(worker_root), worker, name) if ready else None
    if failed:
        ready, why = False, failed
    heartbeat = settings.tasks_dir(worker_root).parent / HEARTBEAT
    serve = heartbeat.exists() and time.time() - heartbeat.stat().st_mtime < 60
    running = slots.held(worker_root)
    repo_ok = not repo.startswith(("/", "~")) or (Path(repo).expanduser() / ".git").exists()
    load = os.getloadavg()[0] if hasattr(os, "getloadavg") else 0.0
    memory_max = settings.setting("memory_max", worker=worker) if _systemd_available() else None
    return {"mem_available_gb": psutil.virtual_memory().available // (1 << 30),
            "runner": name, "ready": ready, **({"runner_at": why} if ready else {"why": why}), "serve": serve, "running": running,
            "memory_max": memory_max,
            "bid_running": slots.held(worker_root, pool="bid"), "named_running": slots.held(worker_root, pool="named"),
            "cores": os.cpu_count() or 1, "load": round(load, 2), "free_gb": _free_gb(worker_root), "repo": repo_ok,
            **model_path(worker_root, name, spec)}


def steer(worker_root: Path, task_id: str, text: str) -> dict:
    task = task_of(worker_root, task_id)
    run = last_run(task)
    if not alive(task, run) or not task.path("handle.json").exists():
        return {"action": "not_live", "run": run}
    runner = runner_for(worker_root, task, job_of(task))
    try:
        handle = Handle.load(task, run)
        if _permission_pending(runner, handle):
            return {"action": "error", "reason": "permission_dialog", "run": run}
        how, _ = runner.follow_up(handle, text)
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


KEYS = "keys.json"
UNCHANGED = ("error", "refuse", "refused", "busy", "not_live")  # replies after which the task is as it was


def _kept(task: Task, key: str | None, act) -> dict:
    """*act* once per request key, under the task's lock: a key seen before, by whichever write verb, returns the
    reply it got then and changes nothing (a retry the hub routes to another verb must not apply the words twice).
    A reply that left the task as it was is not kept, so the same key can try again."""
    if not key:
        return act()
    kept = read_json(task.path(KEYS), {}) or {}
    if key in kept:
        return {**kept[key], "replayed": True}
    reply = act()
    if reply.get("action") not in UNCHANGED:
        write_json(task.path(KEYS), {**kept, key: reply})
    return reply


def keyed(worker_root: Path, args: list[str], act) -> dict:
    """``answer`` and ``send``, whose optional second argument is the hub's request key."""
    key = args[1] if len(args) > 1 else None
    if not key:
        return act()
    task = task_of(worker_root, args[0])
    with task_lock(task):
        return _kept(task, key, act)


def follow_up(worker_root: Path, task_id: str, payload: dict) -> dict:
    """``follow_up``; its payload's ``request_key`` is kept like the key of ``answer`` and ``send``."""
    task = task_of(worker_root, task_id)
    with task_lock(task):
        return _kept(task, payload.get("request_key"), lambda: _follow_up_locked(worker_root, task, task_id, payload))


def _follow_up_locked(worker_root: Path, task: Task, task_id: str, payload: dict) -> dict:
    """A person's follow-up, so that a task has at most one requested-or-running run: queued into the newest run
    while it waits for ``serve``, steered into it while it is live, refused while it is starting; once that run
    ended, the next run is requested."""
    job = job_of(task)
    current = job.get("access", "workspace-write")
    access = payload.get("access", current)
    if current not in ACCESS or access not in ACCESS or ACCESS.index(access) > ACCESS.index(current):
        return {"action": "error", "reason": f"access {access!r} exceeds recorded mode {current!r}"}
    run = slots.requested(task.dir)
    if access != current:
        supported, reason = runner_for(worker_root, task, job).honours(access)
        if not supported:
            return {"action": "error", "reason": reason, "run": run}
        if run and not task.path(f"rc.{run}").exists() and task.path(f"taken.{run}").exists():
            if not alive(task, run) or not task.path("handle.json").exists():
                return {"action": "busy", "run": run}
            live = read_json(task.path(f"live.{run}.json"), {}) or {}
            reply = (answer(worker_root, task_id, payload["message"]) if live.get("state") == "waiting_human"
                     else steer(worker_root, task_id, payload["message"]))
            if reply.get("action") not in ("answer", "steered", "started"):
                return reply
            job["access"] = access
            write_json(task.path("job.json"), job)
            grant = read_json(task.path("grant.json"), {}) or {}
            write_json(task.path("grant.json"), {**grant, "access": access})
            return reply
        job["access"] = access
        write_json(task.path("job.json"), job)
        grant = read_json(task.path("grant.json"), {}) or {}
        write_json(task.path("grant.json"), {**grant, "access": access})
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


def _permission_pending(runner, handle: Handle) -> bool:
    """Only runners with a permission channel can have a pending permission dialog."""
    query = getattr(runner, "permission_pending", None)
    return query is not None and query(handle)


def human(worker_root: Path, task_id: str) -> dict:
    task = task_of(worker_root, task_id)
    run = last_run(task)
    is_alive = alive(task, run)
    if is_alive and task.path("handle.json").exists():
        runner = runner_for(worker_root, task, job_of(task))
        if _permission_pending(runner, Handle.load(task, run)):
            return {"open": False, "permission": True, "run": run}
    contract = read_json(task.path(f"live.{run}.json"), {}) or {}
    reply = {"open": is_alive and contract.get("state") == "waiting_human", "run": run}
    question = task.work / "question.md"  # a command runner's question (runners/command.py)
    if reply["open"] and question.is_file() and not question.is_symlink():
        reply["question"] = question.read_text(encoding="utf-8-sig", errors="replace")[:4000]
    return reply


def answer(worker_root: Path, task_id: str, text: str) -> dict:
    task = task_of(worker_root, task_id)
    run = last_run(task)
    runner = runner_for(worker_root, task, job_of(task))
    try:
        handle = Handle.load(task, run)
        if _permission_pending(runner, handle):
            return {"action": "error", "reason": "permission_dialog", "run": run}
        reply = runner.answer(handle, text)
    except RunnerError as exc:
        reason = str(exc)[:300]
        reply = {"action": "error", "reason": "permission_dialog" if reason.startswith("permission_dialog:") else reason}
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
        for name, relative in PACKED.items():
            source = task.path(relative.format(run=last_run(task)))
            if source.exists() and not source.is_symlink():
                archive.add(str(source), arcname=name, recursive=False)
        for source in iter_artifact_files(task.work / "out"):
            archive.add(str(source), arcname=source.relative_to(task.work).as_posix(), recursive=False)
    return buffer.getvalue()


def bundle(worker_root: Path, task_id: str, ref: str = "HEAD") -> tuple[int, bytes]:
    """``git bundle create - <ref>`` in the task's work folder: the branch with its whole history (merges included),
    for landing it on the hub. Read-only: nothing in the work folder changes."""
    work = task_of(worker_root, task_id).work
    if not (work / ".git").exists():
        return _ok({"action": "error", "reason": "the task has no git work folder"})
    done = subprocess.run(["git", "bundle", "create", "-", ref], cwd=work, capture_output=True, timeout=600, check=False,
                          env=_git_env())
    if done.returncode != 0:
        return _ok({"action": "error", "reason": done.stderr.decode("utf-8", "replace").strip()[-300:]})
    return 0, done.stdout


def _ok(value: dict) -> tuple[int, bytes]:
    code = 1 if value.get("action") == "error" else 0
    return code, (json.dumps(value, ensure_ascii=False) + "\n").encode("utf-8")


def dispatch(worker_root: Path, verb: list[str], stdin: bytes = b"") -> tuple[int, bytes]:
    """One ``_device`` verb: ``(exit code, stdout bytes)``."""
    from hermes_cli.worker.protocol import verbs

    name, args = verb[0], verb[1:]
    try:
        table = verbs()
        if name not in table:
            return _ok({"action": "error", "reason": f"unknown verb {name}"})
        return table[name].handler(worker_root, args, stdin)
    except (IndexError, ValueError, OSError) as exc:
        return _ok({"action": "error", "reason": f"{name}: {exc}"[:300]})


def run_argv(task: Task, run: int) -> list[str]:
    return [sys.executable, "-P", "-m", "hermes_cli.main", "worker", "_run", task.id, str(run)]


def _systemd_available() -> bool:
    return sys.platform.startswith("linux") and locate_command("systemd-run").found


def launch(worker_root: Path, task: Task, run: int) -> None:
    """Start run *run* as its own process: a transient systemd user unit with the memory cap where there is
    systemd, else a new session (``setsid``). Either way a restart of the service leaves it running."""
    argv = run_argv(task, run)
    # The run process is lina itself serving this LINA root: it keeps the service's HERMES_HOME (no profile bridge)
    # and the credentials the runner's agent needs, and finds hermes_cli through PYTHONPATH.
    child_env = build_subprocess_env(scrub_secrets=False, inherit_profile_home=False,
                                     extra={"PYTHONPATH": settings.child_pythonpath()})
    if _systemd_available():
        unit = f"lina-worker-{task.id}-{run}"
        env = [f"--setenv={key}={child_env[key]}" for key in ("HERMES_HOME", "PATH", "HOME", "LANG", "PYTHONPATH")
               if child_env.get(key)]
        env.append(f"--setenv=LINA_HOME={settings.lina_home().resolve()}")
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
    if task.path(f"rc.{run}").exists():
        task.path(f"pid.{run}").unlink(missing_ok=True)
