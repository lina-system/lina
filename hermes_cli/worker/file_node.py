"""A file node: a device the hub drives over ssh with no LINA on it (``kind: file`` in ``devices.workers``).

The node has bash, omo (with its daemon) and coreutils, but no git, no service manager and no recent Python, so it
cannot run ``lina worker _device``. ``FileNodeTransport`` answers the same verbs on the hub instead: it keeps each
task in the device layout under ``<worker root>/file_nodes/<device>/tasks/<ID>/`` (job, prompts, slots, state,
manifest) and turns the verbs that need the node into short bash scripts (``file_node_bash``) and omo calls there.
On the node a task is ``$HOME/<root>/<ID>/`` with ``work/`` (``input/``, ``out/``, ``report.md``) and one
``runner.sh`` per run, started with ``nohup setsid`` so it outlives the ssh call; it drives the omo session and
leaves end marks, and the hub judges the end from them (``meta``). See docs/file-node.md.
"""

from __future__ import annotations

import io
import json
import shlex
import shutil
import subprocess
import tarfile
import time
from pathlib import Path

from hermes_cli.worker import file_node_bash as bash
from hermes_cli.worker import file_node_deny as deny
from hermes_cli.worker import settings, slots
from hermes_cli.worker.runners.base import Task
from hermes_cli.worker.task import end_state, lock_inputs, read_json, safe_id, write_json, write_manifest
from hermes_cli.worker.transport import Reply

ACCESS_MODES = ("full",)  # the run reaches the whole volume: no narrower mode is enforced there
VERBS = ("hello", "probe", "reserve", "put", "untar", "meta", "send", "follow_up", "human", "answer", "cancel", "pack")
MIRROR = "file_nodes"
SEALED = ("done", "failed", "cancelled")


def _answer(value: dict) -> Reply:
    return Reply(1 if value.get("action") == "error" else 0, (json.dumps(value, ensure_ascii=False) + "\n").encode())


def _fields(reply: Reply) -> dict[str, str]:
    lines = reply.out.decode("utf-8", "replace").splitlines()
    return dict(line.split("=", 1) for line in lines if "=" in line)


def _read(task: Task, name: str) -> str:
    try:
        return task.path(name).read_text(encoding="utf-8-sig", errors="replace").strip()
    except OSError:
        return ""


def _model_arg(model: dict | None) -> str:
    """``provider/id`` for ``omo host session open --model`` (it splits on the one ``/``); "" leaves omo's own."""
    provider, ident = (model or {}).get("provider"), (model or {}).get("id")
    return f"{provider}/{ident}" if provider and ident and "/" not in ident else ""


def _final_text(messages: dict | None) -> str:
    text = ""
    for message in (messages or {}).get("messages") or []:
        content = message.get("content") if isinstance(message, dict) else None
        if message.get("role") == "assistant" and isinstance(content, list):
            said = "".join(part.get("text", "") for part in content
                           if isinstance(part, dict) and part.get("type") == "text").strip()
            text = said or text
    return text


def _live(line: str) -> dict:
    from hermes_cli.worker.runners.omo_settle import lifecycle_view
    try:
        view = lifecycle_view(json.loads(line)) if line else None
    except ValueError:
        view = None
    if not view:
        return {}
    asking = view.get("state") == "waiting_human" or bool(view.get("pendingQuestions"))
    return {"lifecycle": view.get("state"), "dash": "human" if asking else None}


class FileNodeTransport:
    def __init__(self, device: settings.Device, worker_root: Path, connect_timeout: int = 10):
        self.device, self.worker_root, self.connect_timeout = device, worker_root, connect_timeout
        self.mirror = worker_root / MIRROR / device.name

    def call(self, verb: list[str], stdin: bytes = b"", timeout: float = 120) -> Reply:
        handlers = {"hello": self._hello, "probe": self._probe, "reserve": self._reserve, "put": self._put,
                    "untar": self._untar, "meta": self._meta, "send": self._send, "follow_up": self._follow_up,
                    "human": self._human, "answer": self._no_answer, "cancel": self._cancel, "pack": self._pack}
        handler = handlers.get(verb[0])
        if handler is None:
            return _answer({"action": "error", "reason": f"a file node has no verb {verb[0]}"})
        try:
            return handler(verb[1:], stdin, timeout)
        except (IndexError, ValueError, OSError, tarfile.TarError) as exc:
            return _answer({"action": "error", "reason": f"{verb[0]}: {exc}"[:300]})

    def rsh(self, script: str, *args, stdin: bytes = b"", timeout: float = 120) -> Reply:
        """*script* under bash on the node (its login shell is not bash), *args* as ``$1…``."""
        remote = shlex.join(["bash", "-c", script, "lina-file-node", *(str(arg) for arg in args)])
        argv = ["ssh", "-o", "BatchMode=yes", "-o", f"ConnectTimeout={self.connect_timeout}", self.device.host, remote]
        try:
            done = subprocess.run(argv, input=stdin, capture_output=True, timeout=timeout, check=False)
        except subprocess.TimeoutExpired:
            return Reply(124, b"", f"{self.device.name}: no answer in {timeout:.0f}s")
        except OSError as exc:
            return Reply(255, b"", str(exc))
        return Reply(done.returncode, done.stdout, done.stderr.decode("utf-8", "replace"))

    def _task(self, task_id: str) -> Task:
        return Task(safe_id(task_id), settings.tasks_dir(self.mirror) / task_id)

    def _remote(self, task_id: str) -> str:
        return f"{self.device.root}/{safe_id(task_id)}"

    def facts(self, timeout: float = 60) -> tuple[Reply, dict]:
        """The node's omo, model path, disk and load; the second item adds ``ready``/``why`` and ``model_path``."""
        dev = self.device
        reply = self.rsh(bash.FACTS, dev.omo, dev.model_check, timeout=timeout)
        found = _fields(reply)
        code = found.get("model", "")
        view = {"model_path": "ok" if code == "200" else "down", "omo": found.get("omo") or None}
        if code != "200":
            view["model_path_why"] = f"model path through the hub does not answer ({dev.model_check}: HTTP {code or 'none'})"
        why = f"omo not found on the node ({dev.omo})" if not view["omo"] else view.get("model_path_why")
        view.update(ready=why is None, **({"why": why} if why else {}))
        return reply, {**found, **view}

    def _hello(self, args, data, timeout) -> Reply:
        from hermes_cli.lina_version import LINA_VERSION
        from hermes_cli.worker.protocol import PROTOCOL_MAX, PROTOCOL_MIN
        reply, found = self.facts(timeout)
        if reply.code != 0:
            return reply
        runner = {"name": "omo", "type": "omo", "pin": None, "pinned": None, "installed": found["omo"],
                  "sign_in": None, "ready": found["ready"], "why": found.get("why")}
        return _answer({"protocol": {"min": PROTOCOL_MIN, "max": PROTOCOL_MAX}, "instance_id": f"file-node:{self.device.name}",
                        "serve": True, "version": LINA_VERSION, "kind": settings.FILE_KIND, "role": self.device.role,
                        "bash": found.get("bash"),
                        **{key: found[key] for key in ("omo", "model_path", "model_path_why", "ready", "why") if key in found},
                        "capabilities": {"runners": [runner], "access_modes": list(ACCESS_MODES), "verbs": list(VERBS)}})

    def _probe(self, args, data, timeout) -> Reply:
        from hermes_cli.worker import runners
        reply, found = self.facts(timeout)
        if reply.code != 0:
            return reply
        worker = settings.section(self.worker_root.parent)
        name = (args[1] if len(args) > 1 else "") or runners.default_name(worker)
        kind = (runners.specs(worker).get(name) or {}).get("type")
        ready, why = found["ready"], found.get("why")
        if ready and kind != "omo":
            ready, why = False, f"a file node runs omo only (runner {name} is {kind})"
        free_kb = found.get("free_kb", "")
        return _answer({"runner": name, "ready": ready, **({"why": why} if why else {"runner_at": found["omo"]}),
                        "serve": True, "running": slots.held(self.mirror), "memory_max": None,
                        "bid_running": slots.held(self.mirror, pool="bid"),
                        "named_running": slots.held(self.mirror, pool="named"),
                        "cores": int(found.get("cores") or 1), "load": float(found.get("load") or 0),
                        "free_gb": int(free_kb) >> 20 if free_kb.isdigit() else 0, "repo": True,
                        "kind": settings.FILE_KIND, "model_path": found["model_path"],
                        **({"model_path_why": found["model_path_why"]} if "model_path_why" in found else {})})

    def _reserve(self, args, data, timeout) -> Reply:
        return _answer(slots.reserve(self.mirror, safe_id(args[0]), int(args[1]),
                                     pool=args[2] if len(args) > 2 else "bid"))

    def _put(self, args, data, timeout) -> Reply:
        from hermes_cli.worker import device
        task_id, rel = args[0], args[1]
        reply = device._put(self.mirror, task_id, rel, data)
        if reply.get("action") == "error" or not rel.startswith("request."):
            return _answer(reply)
        return _answer(self.launch(task_id, int(rel.split(".", 1)[1])))

    def _untar(self, args, data, timeout) -> Reply:
        from hermes_cli.worker import device
        return _answer(device._untar(self.mirror, args[0], args[1], data))

    def launch(self, task_id: str, run: int) -> dict:
        """Send run *run*'s files to the node and start its runner there, in its own session."""
        task = self._task(task_id)
        job = read_json(task.path("job.json"), {}) or {}
        if job.get("repo"):
            return {"action": "error", "reason": "a file node takes no repository work"}
        upload = task.path("input.upload")
        if run == 1 and upload.is_dir():
            task.work.mkdir(parents=True, exist_ok=True)
            shutil.move(str(upload), str(task.work / "input"))
            lock_inputs(task)
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w") as archive:
            for name in ("id", f"prompt.{run}"):
                archive.add(str(task.path(name)), arcname=name)
            script = tarfile.TarInfo("runner.sh")
            script.size, script.mode = len(bash.RUNNER.encode()), 0o755
            archive.addfile(script, io.BytesIO(bash.RUNNER.encode()))
            if run == 1 and (task.work / "input").is_dir():
                archive.add(str(task.work / "input"), arcname="work/input")
        worker = settings.section(self.worker_root.parent)
        poll = float(settings.setting("poll_s", worker=worker))
        hard = int(float(settings.setting("hard_timeout_s", worker=worker)))
        model = _model_arg(job.get("model"))
        reply = self.rsh(bash.LAUNCH, self._remote(task_id), run, self.device.omo, poll, hard, *([model] if model else []),
                         stdin=buffer.getvalue(), timeout=300)
        if reply.code != 0 or "started" not in _fields(reply):
            return {"action": "error", "reason": f"start on {self.device.name}: "
                                                 f"{' '.join(reply.err.split())[-300:] or f'exit {reply.code}'}"}
        task.path(f"taken.{run}").write_text(f"{time.time():.3f}\n", encoding="utf-8")
        task.event(run, "started", runner="omo", node=self.device.name, pid=_fields(reply)["started"],
                   access=job.get("access"))
        return {"action": "put", "launched": run}

    def _meta(self, args, data, timeout) -> Reply:
        task = self._task(args[0])
        if not task.dir.is_dir():
            return _answer({"missing": True})
        run = slots.requested(task.dir)
        state, alive, live = _read(task, "state"), False, {}
        if run and task.path(f"taken.{run}").exists() and not task.path(f"rc.{run}").exists():
            reply = self.rsh(bash.STATUS, self._remote(task.id), run, timeout=timeout)
            if reply.code != 0:
                return reply
            found = _fields(reply)
            if found.get("alive") == "1" and not found.get("rc"):
                state, alive, live = "running", True, _live(found.get("lifecycle", ""))
            else:
                state = self.finish(task, run, gone=not found.get("rc"))
        elif run and not task.path(f"rc.{run}").exists():
            state = "new"
        return _answer({"state": state or "new", "run": run, "requested": run, "rc": _read(task, f"rc.{run}"),
                        "alive": alive, "report": (task.work / "report.md").is_file(),
                        "final": _read(task, "final.md")[:600], "live": live})

    def finish(self, task: Task, run: int, *, gone: bool = False) -> str:
        """Run *run* ended on the node: copy what it left, judge it here, record the end (``rc.<n>`` last)."""
        from hermes_cli.worker.device import task_lock
        with task_lock(task):
            if task.path(f"rc.{run}").exists():
                return _read(task, "state")
            rc = "1"
            if not gone:
                rc = self._collect(task, run)
            runner_state, why = self._runner_state(task, run, gone)
            write_json(task.path(f"wait.{run}.json"), {"outcome": _read(task, f"outcome.{run}") or "gone",
                                                      "via": "file node", **({"why": why} if why else {})})
            if why:
                with task.path(f"stderr.{run}.txt").open("a", encoding="utf-8") as fh:
                    fh.write(f"lina worker: {why}\n")
            task.path("final.md").write_text(_final_text(read_json(task.path(f"messages.{run}.json"))) + "\n",
                                             encoding="utf-8")
            check = self._deny_check(task, run) if self.device.deny and not gone else None
            manifest = write_manifest(task, run)
            wanted, now = read_json(task.path("input.manifest.json")), self._input_hashes(task, run)
            if wanted is not None:
                manifest["inputs_unchanged"] = now == wanted
            job = read_json(task.path("job.json"), {}) or {}
            final = end_state(runner_state, manifest, cancelled=task.path("cancelled").exists(),
                              next_notes_required=bool(job.get("next_notes_rule", True)))
            if check is not None:
                if deny.needs_review(check) and final in ("done", "done_empty", "review"):
                    check["forced_review"], final = final != "review", "review"
                manifest["deny_check"] = check
            write_json(task.path("manifest.json"), manifest)
            task.path("state").write_text(final + "\n", encoding="utf-8")
            task.event(run, "ended", state=final, runner_state=runner_state)
            task.path(f"rc.{run}").write_text(rc + "\n", encoding="utf-8")
            return final

    def _collect(self, task: Task, run: int) -> str:
        reply = self.rsh(bash.COLLECT, self._remote(task.id), run, timeout=600)
        if reply.code != 0:
            raise OSError(f"collect run {run}: {' '.join(reply.err.split())[-300:] or f'exit {reply.code}'}")
        shutil.rmtree(task.work / "out", ignore_errors=True)
        (task.work / "report.md").unlink(missing_ok=True)
        rc = "1"
        with tarfile.open(fileobj=io.BytesIO(reply.out), mode="r") as archive:
            members = []
            for member in archive.getmembers():
                if member.name == f"rc.{run}":
                    rc = (archive.extractfile(member) or io.BytesIO(b"1")).read().decode().strip() or "1"
                else:
                    members.append(member)
            archive.extractall(task.dir, members=members, filter="data")
        return rc

    def _runner_state(self, task: Task, run: int, gone: bool) -> tuple[str, str]:
        from hermes_cli.worker.runners.omo_settle import lifecycle_view
        outcome = _read(task, f"outcome.{run}")
        if task.path("cancelled").exists() or outcome == "cancelled":
            return "cancelled", ""
        if gone:
            return "failed", f"the runner on {self.device.name} ended without its end marks"
        if outcome == "start_failed":
            return "failed", "omo did not open or prompt the session"
        if outcome == "timeout":
            return "stalled", "the run reached hard_timeout_s"
        view = lifecycle_view(read_json(task.path(f"state.{run}.json")))
        if outcome != "sealed" or view is None:
            return "failed", f"the runner ended with outcome {outcome or 'none'}"
        if view.get("no_lifecycle"):
            return "done", ""
        state = view.get("state")
        return (state, "") if state in SEALED else ("stalled", f"lifecycle {state} is not sealed")

    def _input_hashes(self, task: Task, run: int) -> dict[str, str] | None:
        try:
            lines = task.path(f"inputs.{run}.sha256").read_text(encoding="utf-8-sig", errors="replace").splitlines()
        except OSError:
            return None
        hashes = {}
        for line in lines:
            digest, _, name = line.partition("  ")
            if name:
                hashes[f"input/{name.removeprefix('./')}"] = digest
        return hashes

    def _deny_check(self, task: Task, run: int) -> dict:
        limit = self.device.scan_s
        reply = self.rsh(bash.DENY_SCAN, self._remote(task.id), run, limit, *deny.scan_args(self.device),
                         timeout=limit + 60)
        check = deny.parse_scan(reply.out.decode("utf-8", "replace"), reply.code, limit)
        write_json(task.path(f"deny.{run}.json"), check)
        return check

    def _live_now(self, task: Task, run: int) -> bool:
        """Run *run* is still going on the node (finishing it here first when it ended)."""
        if not run or not task.path(f"taken.{run}").exists() or task.path(f"rc.{run}").exists():
            return False
        reply = self._meta([task.id], b"", 90)
        return reply.code == 0 and bool(reply.json().get("alive"))

    def _send(self, args, data, timeout) -> Reply:
        task = self._task(args[0])
        run = slots.requested(task.dir)
        if not self._live_now(task, run):
            return _answer({"action": "not_live", "run": run})
        return self._steer(task, run, data, timeout)

    def _steer(self, task: Task, run: int, data: bytes, timeout: float) -> Reply:
        reply = self.rsh(bash.STEER, self._remote(task.id), self.device.omo, stdin=data, timeout=timeout)
        if reply.code != 0:
            return _answer({"action": "error", "run": run,
                            "reason": (reply.json().get("reason") or ' '.join(reply.err.split()) or f"exit {reply.code}")[-300:]})
        task.event(run, "follow_up", how="steered")
        return _answer({"action": "steered", "run": run})

    def _follow_up(self, args, data, timeout) -> Reply:
        task, payload = self._task(args[0]), json.loads(data)
        if payload.get("access", ACCESS_MODES[0]) not in ACCESS_MODES:
            return _answer({"action": "error", "reason": f"a file node runs with access {ACCESS_MODES[0]} only"})
        run = slots.requested(task.dir)
        if self._live_now(task, run):
            return self._steer(task, run, payload["message"].encode("utf-8"), timeout)
        if run and not task.path(f"rc.{run}").exists():
            return _answer({"action": "busy", "run": run})
        run += 1
        task.path(f"prompt.{run}").write_text(payload["prompt"], encoding="utf-8")
        task.path(f"request.{run}").write_text(json.dumps({"run": run, "at": time.time()}), encoding="utf-8")
        started = self.launch(task.id, run)
        return _answer(started if started.get("action") == "error" else {"action": "requested", "run": run})

    def _human(self, args, data, timeout) -> Reply:
        """No answer channel to omo's questions on a file node: a question is left to a follow-up or a cancel."""
        return _answer({"open": False, "run": slots.requested(self._task(args[0]).dir)})

    def _no_answer(self, args, data, timeout) -> Reply:
        return _answer({"action": "error", "reason": "a file node has no answer channel: send a follow-up instead"})

    def _cancel(self, args, data, timeout) -> Reply:
        task = self._task(args[0])
        if not task.dir.is_dir():
            return _answer({"action": "cancel", "missing": True})
        run, why = slots.requested(task.dir), data.decode("utf-8", "replace") or "cancelled"
        task.path("cancelled").write_text(why + "\n", encoding="utf-8")
        alive = False
        if run and task.path(f"taken.{run}").exists() and not task.path(f"rc.{run}").exists():
            reply = self.rsh(bash.CANCEL, self._remote(task.id), self.device.omo, stdin=why.encode("utf-8"),
                             timeout=timeout)
            if reply.code != 0:
                return _answer({"action": "error", "reason": f"cancel on {self.device.name}: "
                                                             f"{' '.join(reply.err.split())[-300:] or f'exit {reply.code}'}"})
            alive = self._live_now(task, run)
        elif run and not task.path(f"rc.{run}").exists():
            task.path("state").write_text("cancelled\n", encoding="utf-8")
            task.path(f"rc.{run}").write_text("0\n", encoding="utf-8")
        task.event(run, "cancel", why=why[:300])
        return _answer({"action": "cancel", "run": run, "alive": alive})

    def _pack(self, args, data, timeout) -> Reply:
        from hermes_cli.worker import device
        return Reply(0, device.pack(self.mirror, safe_id(args[0])))


def submit_refusal(dev: settings.Device, worker_root: Path, texts: list[str], inputs: list[str]) -> str | None:
    """Why a run named onto file node *dev* is refused at submit: a denied folder in a brief or an input, or a
    model path through the hub that does not answer now."""
    if found := deny.submit_refusal(dev, texts, inputs):
        return found
    reply, facts = FileNodeTransport(dev, worker_root).facts()
    if reply.code != 0:
        return f"{dev.name}: unreachable: {' '.join(reply.err.split())[-160:] or f'exit {reply.code}'}"
    return None if facts["ready"] else f"{dev.name}: {facts['why']}"
