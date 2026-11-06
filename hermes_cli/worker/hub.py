"""The hub side of ``lina worker``: submit, send, status, result, accept, reject, cancel, list, receipts, hosts,
schedules, the tick and the end notice.

A task goes to the device that wins the bid (or the one named with ``--host``), or waits in the queue until one is
free. Its first prompt carries the mode prefix and the report contract; a run started from a work card is recorded on
that card. When a run ends the device reports it (``_notify``); the hub fetches the results, checks them against the
device's manifest and sends one notice. ``tick`` (run by the service on the hub) catches what a device could not
report.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import subprocess
import time
from pathlib import Path

from hermes_cli.lina_work_settings import ACCESS, SettingsError
from hermes_cli.lina_work_settings_route import decide
from hermes_cli.worker import bidding, kanban_link, models, notice, request_keys, runners, settings
from hermes_cli.worker.hub_store import Store
from hermes_cli.worker.report import NEXT_NOTES, first_prompt, follow_up_prompt, with_hub_rules
from hermes_cli.worker.runners.base import RunnerError
from hermes_cli.worker.task import read_json, verify
from hermes_cli.worker.transport import TransportError, fetch, for_device, put, put_tree

ENDED = ("done", "done_empty", "review", "stalled", "failed", "cancelled")
VERDICT_WORDS = {None: "no verdict yet", "accepted": "accepted", "rejected": "rejected"}
NEXT_RUN_WORDS = {
    "answer": "{id} answered the question of run {run}; the same run goes on.",
    "queued": "{id} run {run} has not started yet: the instruction was added to it.",
    "steered": "{id} took the instruction into run {run}: it applies after the current tool call, before the next "
               "decision.",
    "started": "{id} took the instruction into run {run}: it applies after the current tool call, before the next "
               "decision.",
    "requested": "{id} started run {run} in the same session. The end notice will come.",
}
HUMAN_NOTICE_AFTER_S = 120
ANSWER_CLOSE_WAIT_S = 150
# A Hermes card run's result fetch: tried on each report and tick (30 s) before its end goes out without report.md.
FETCH_TRIES = 10


def _start_time(pid: int) -> int | None:
    """The process's start fingerprint (the repo's canonical one); ``None`` when it is gone or unreadable."""
    from gateway.status import get_process_start_time
    return get_process_start_time(pid)


class HubError(RuntimeError):
    pass


def caller_agent() -> str:
    from hermes_cli.profiles import get_active_profile_name
    return get_active_profile_name() or "default"


def owner(bot: str | None = None) -> tuple[str, str]:
    """The agent a call is made for, and where the name came from: ``--bot``, else ``HERMES_PROFILE``, else
    ``HERMES_PROFILE_NAME``, else the profile the caller's Hermes Agent ``HERMES_HOME`` names
    (``<root>/profiles/<name>``; the root itself is ``default``), else LINA's active profile."""
    if bot:
        return bot, "--bot"
    for name in ("HERMES_PROFILE", "HERMES_PROFILE_NAME"):
        value = os.environ.get(name, "").strip()
        if value:
            return value, name
    home = settings.caller_hermes_home()
    if home is not None:
        return (home.name if home.parent.name == "profiles" else "default"), "HERMES_HOME"
    return caller_agent(), "current profile"


def _question_text(item) -> str:
    """The words of a pending question as the runner's live view carries it."""
    if isinstance(item, str):
        return item.strip()
    if isinstance(item, list):
        return "\n".join(text for text in map(_question_text, item) if text)
    if isinstance(item, dict):
        for key in ("question", "text", "prompt", "title", "message"):
            if isinstance(item.get(key), str) and item[key].strip():
                return item[key].strip()
        return _question_text(item.get("questions") or [])
    return ""


def _ago(at: float | None) -> str:
    if not at:
        return "-"
    seconds = int(time.time() - float(at))
    return f"{seconds // 3600}h {seconds % 3600 // 60}m" if seconds >= 3600 else \
        f"{seconds // 60}m" if seconds >= 60 else f"{seconds}s"


class Hub:
    def __init__(self, worker_root: Path, out=print):
        self.worker_root, self.out = worker_root, out
        self.base = worker_root.parent
        self.store = Store(worker_root)
        self.worker = settings.section(self.base)
        self.fetched: bool | None = None


    def devices(self) -> list[settings.Device]:
        return settings.devices(self.base)

    def device(self, name: str) -> settings.Device:
        for dev in self.devices():
            if dev.name == name:
                return dev
        raise HubError(f"no device {name!r} in devices.workers")

    def transport(self, job: dict):
        return for_device(self.device(job["host"]), self.worker_root)

    def need(self, task_id: str) -> dict:
        job = self.store.load(task_id)
        if not job:
            raise HubError(f"{task_id}: no such task")
        return job


    def make_job(self, brief: Path, opts: dict, group: str | None, tid: str | None = None) -> dict:
        agent = opts["bot"]
        tid = tid or self.store.new_id(agent)
        branch = f"omo/{tid}" if opts.get("repo") else None
        try:
            decision = decide(self.base, opts.get("runner"), opts["mode"], opts.get("model"), opts.get("labels"),
                              opts.get("size"))
        except SettingsError as exc:
            raise HubError(str(exc)) from exc
        runner = decision.runner
        spec = runners.specs(self.worker).get(runner)
        if spec is None:
            raise HubError(f"no runner {runner!r} (configured: {', '.join(runners.specs(self.worker))})")
        access = opts.get("access", "workspace-write")
        ceiling = self.worker.get("access", {}).get(agent, "workspace-write")
        if access not in ACCESS or ceiling not in ACCESS:
            raise HubError(f"invalid access mode or ceiling: {access!r}, {ceiling!r}")
        if ACCESS.index(access) > ACCESS.index(ceiling):
            raise HubError(f"access {access} exceeds worker.access.{agent} ceiling {ceiling}")
        supported, reason = runners.build(runner, spec).honours(access)
        if not supported:
            raise HubError(reason)
        model = decision.model
        grant = {"access": access, "workspace": "work/ of the task folder", "inputs_readonly": True, "push": False,
                 "private_read_in_place": opts.get("private_inputs") or [],
                 "repo": opts.get("repo"), "outside_effects": "report only (messages, publish, delete, money, "
                                                              "credentials need the owner)"}
        job = {"id": tid, "bot": agent, "topic": opts["topic"], "host": None, "fixed_host": opts.get("host"),
               "repo": opts.get("repo"), "branch": branch, "mode": opts["mode"], "access": access,
               "run_access": {"1": access}, "brief": str(brief),
               "todo": opts.get("todo"), "runner": runner, "runner_spec": spec,
               "model": model.as_dict() if model else None, "created": time.time(), "runs": 1, "notified": [],
               "extra": opts.get("note"), "dry_notify": bool(opts.get("dry_notify")), "inputs": opts["inputs"],
               "prompt": first_prompt(opts["mode"], brief.read_text(encoding="utf-8-sig"), branch)
               + _private_lines(opts.get("private_inputs") or []), "state": "new",
               "private_inputs": opts.get("private_inputs") or [],
               "grant": grant, "delivery": "pending", "verdict": None, "group": group, "next_notes_rule": True,
               "card": opts.get("card"), "card_start_pending": kanban_link.on_hermes(opts.get("card")),
               "settings": decision.settings, "request_key": opts.get("request_key")}
        self.store.save(job)
        self.store.receipt(tid, "submitted", bot=agent, topic=job["topic"], mode=job["mode"], runner=runner,
                           model=job["model"], brief=str(brief), repo=job["repo"], group=group, grant=grant,
                           card=job["card"], input_id=f"{tid}.1", settings=decision.settings, access=access,
                           request_key=job["request_key"])
        if decision.fallback:
            self.store.receipt(tid, "model_fallback", **decision.fallback)
        return job

    def _link_card(self, job: dict) -> None:
        if not job.get("card"):
            return
        if kanban_link.on_hermes(job["card"]) and "HERMES_DELEGATED_CHILD_CONTEXT" in os.environ:
            return
        try:
            kanban_link.record_run(job["card"], job, self._link())
        except (ValueError, OSError, kanban_link.CardLinkError) as exc:
            self.store.receipt(job["id"], "card_link_failed", error=str(exc)[:300])
            self.out(f"lina worker: could not record {job['id']} on card {job['card']['task']}: {exc}")
            return
        self.store.receipt(job["id"], "card_linked", card=job["card"])
        if job.get("card_start_pending"):
            job.pop("card_start_pending")
            self.store.save(job)

    def _notify_spec(self, dev: settings.Device) -> dict:
        if dev.local:
            return {"via": "local"}
        hub = self.worker.get("hub") if isinstance(self.worker.get("hub"), dict) else {}
        return {"via": "ssh", "host": hub.get("host"), "lina": hub.get("lina") or "lina"}

    def start_on(self, job: dict, dev: settings.Device) -> None:
        """Record the placement durably (host, run 1, ``placing``), then publish it: a hub that dies in between
        resumes this placement (``resume_placement``) instead of bidding the task out a second time."""
        job.update(self.store.update(job["id"], host=dev.name, runs=1, state="placing", award=job.get("award")) or {})
        self.store.receipt(job["id"], "placed", run=1, host=dev.name, award=job.get("award"))
        self._publish(job, dev)

    def resume_placement(self, task_id: str) -> str | None:
        """Finish a placement a hub recorded but may not have published (it stopped in between): the same device
        and run, the request published once. The host, or None when *task_id* is not being placed."""
        job = self.need(task_id)
        if job.get("state") != "placing" or not job.get("host"):
            return None
        dev = self.device(job["host"])
        self._publish(job, dev, resume=True)
        return dev.name

    def _publish(self, job: dict, dev: settings.Device, resume: bool = False) -> None:
        transport, tid = for_device(dev, self.worker_root), job["id"]
        if resume and transport.call(["meta", tid], timeout=90).json().get("requested"):
            self._started(job, dev)  # the request reached the device before the hub stopped
            return
        # The last gate on every path (bid, fixed host, resume): a node that does not advertise the run's access mode
        # would ignore it and run with its old, wider permissions.
        if not transport.descriptor:
            transport.refresh(timeout=90)
        if why := bidding.missing_access(transport.descriptor, job.get("access")):
            raise HubError(f"{dev.name} {why}")
        device_job = {key: job.get(key) for key in ("id", "mode", "access", "runner", "runner_spec", "model", "repo", "branch",
                                                     "next_notes_rule")}
        device_job["notify"] = self._notify_spec(dev)
        put(transport, tid, "job.json", json.dumps(device_job, ensure_ascii=False))
        put(transport, tid, "id", tid + "\n")
        put(transport, tid, "grant.json", json.dumps(job["grant"], ensure_ascii=False) + "\n")
        put(transport, tid, "prompt.1", with_hub_rules(job["prompt"], settings.hub_rules(self.worker))
            if dev.local else job["prompt"])
        if job.get("inputs"):
            put_tree(transport, tid, "input.upload", [Path(p) for p in job["inputs"]])
        put(transport, tid, "request.1", json.dumps({"run": 1, "at": time.time()}))
        self._started(job, dev)

    def _started(self, job: dict, dev: settings.Device) -> None:
        job.update(self.store.update(job["id"], state="running", started=time.time()) or {})
        self.store.receipt(job["id"], "run_started", run=1, host=dev.name, award=job.get("award"))

    def place(self, job: dict) -> str | None:
        """Start *job* on its fixed device or on the bid winner; None when it stays queued.

        One placement at a time from this hub, and the slot is reserved on the device before the task is handed
        over: a batch or two hubs placing at once cannot all land on a device's last slot."""
        with self.store.lock("place"):
            return self._place_locked(job)

    def _reserve(self, job: dict, dev: settings.Device) -> bool:
        named = bool(job.get("fixed_host"))
        capacity = dev.manual_cap if named else dev.cap
        reply = for_device(dev, self.worker_root).call(
            ["reserve", job["id"], str(capacity), *(["named"] if named else [])], timeout=60).json()
        if reply.get("action") != "reserved":
            self.store.log(f"{job['id']} {dev.name}: no slot ({reply.get('held')}/{capacity} held)")
        return reply.get("action") == "reserved"

    def _place_locked(self, job: dict) -> str | None:
        table = None
        if job.get("fixed_host"):
            dev, table = bidding.bid([self.device(job["fixed_host"])], self.worker_root, repo=job.get("repo"),
                                     runner=job["runner"], manual=True, access=job.get("access"),
                                     min_free_gb=int(settings.setting("min_free_gb", worker=self.worker)))
        else:
            dev, table = bidding.bid(self.devices(), self.worker_root, repo=job.get("repo"), runner=job["runner"],
                                     access=job.get("access"),
                                     min_free_gb=int(settings.setting("min_free_gb", worker=self.worker)))
            self.store.log(f"{job['id']} bid: " + "; ".join(
                f"{r['host']} {r.get('running', '-')}/{r['cap']} {r.get('why', 'ok')}" for r in table)
                + f" -> {dev.name if dev else 'queued'}")
        if dev is not None and not self._reserve(job, dev):
            dev = None
        if dev is None:
            if job.get("state") != "queued":
                job.update(self.store.update(job["id"], state="queued") or {})
                self.store.receipt(job["id"], "queued", bids=table)
            return None
        job["award"] = f"AW-{job['id']}-{int(time.time())}"
        self.store.receipt(job["id"], "award", award=job["award"], host=dev.name, fixed=bool(job.get("fixed_host")),
                           bids=[{k: r.get(k) for k in ("host", "running", "cap", "load", "free_gb", "why")}
                                 for r in table or []])
        self.start_on(job, dev)
        return dev.name

    def _link(self) -> dict:
        try:
            return kanban_link.config(self.worker)
        except kanban_link.CardLinkError as exc:
            raise HubError(str(exc)) from exc

    def _hub_node(self, host: str | None) -> str:
        """The device a run on the owner's private files goes to: a local device (the hub itself), never another."""
        local = [dev for dev in self.devices() if dev.local]
        if host is None and local:
            return local[0].name
        if host is not None and any(dev.name == host for dev in local):
            return host
        raise HubError("--input names the owner's private files: they are read only on the hub node, in place "
                       f"({'--host ' + ' or '.join(d.name for d in local) if local else 'no local device in devices.workers'})")

    def _inputs(self, inputs: list[str], private_inputs: list[str]) -> list[str]:
        private = [Path(p).expanduser().resolve() for p in self.worker.get("private_paths") or []]
        limit = int(settings.setting("input_max_mb", worker=self.worker))
        checked = []
        for raw in inputs:
            path = Path(raw).expanduser().resolve()
            if not path.exists():
                raise HubError(f"--input not found: {path}")
            if any(path == p or p in path.parents for p in private):
                private_inputs.append(str(path))  # read in place on the hub node, never copied to a device
                continue
            size = sum(f.stat().st_size for f in path.rglob("*") if f.is_file()) if path.is_dir() else \
                path.stat().st_size
            if size > limit * (1 << 20):
                raise HubError(f"--input too big: {path} {size >> 20}MB > {limit}MB")
            checked.append(str(path))
        return checked

    def submit(self, opts: dict) -> int:
        briefs = [Path(b).expanduser().resolve() for b in opts["briefs"]]
        if not briefs:
            raise HubError("--brief <file> is required")
        for brief in briefs:
            if not brief.is_file():
                raise HubError(f"brief not found: {brief}")
        self._submit_identity(opts)
        opts["topic"] = opts.get("topic") or opts.get("todo") or (opts["card"] or {}).get("task")
        if not opts["topic"]:
            raise HubError("--todo <to-do ID> is required (or --topic <short name> for work without a to-do)")
        if opts.get("request_key"):
            return request_keys.submit(self, opts, briefs)
        if opts.get("at") or opts.get("every"):
            return self.schedule(opts)
        return self.handed_out(opts, self.hand_out(opts, briefs))

    def hand_out(self, opts: dict, briefs: list[Path], ids: list[str] | None = None,
                 group: str | None = None) -> list[dict]:
        """Make and place one task per brief (under *ids* when the IDs were taken before)."""
        opts["private_inputs"] = []
        opts["inputs"] = self._inputs(opts.get("inputs") or [], opts["private_inputs"])
        if opts["private_inputs"]:
            opts["host"] = self._hub_node(opts.get("host"))
        group = group or (self.store.new_id(opts["bot"], "OG") if len(briefs) > 1 else None)
        jobs = [self._submit_one(brief, opts, group, tid) for brief, tid in zip(briefs, ids or [None] * len(briefs))]
        for job in jobs if opts.get("todo") else []:
            self._todo(job, "link", opts["todo"], f"omo:{job['id']}")
        return jobs

    def handed_out(self, opts: dict, jobs: list[dict]) -> int:
        """Where the end of the handed-out *jobs* will be said, or wait for it (``--wait``)."""
        group = jobs[0].get("group")
        if group:
            self.out(f"{group} batch of {len(jobs)} · one notice when all of them end.")
        elif opts.get("wait") is None and kanban_link.on_hermes(opts["card"]):
            self.out(f"The end lands on card {opts['card']['task']}. Run `lina worker wait --card "
                     f"{opts['card']['task']}" + (f" --board {opts['card']['board']}" if opts['card'].get('board') else "")
                     + "` and follow its kanban_schedule instruction.")
        elif opts.get("wait") is None:
            self.out(f"The end notice goes to {opts['bot']}'s chat. Do not poll.")
        if opts.get("wait") is not None and len(jobs) == 1:
            return self._wait(jobs[0]["id"], float(opts["wait"]))
        return 0

    def _submit_identity(self, opts: dict) -> None:
        link = self._link()
        opts["card"] = kanban_link.card_for(opts.get("card") or kanban_link.card_from_env(), link)
        if not opts["card"] and link["kind"] == kanban_link.HERMES and \
                "HERMES_DELEGATED_CHILD_CONTEXT" in os.environ:
            raise HubError("Hermes card session: pass --card <id> --board <slug>")
        opts["bot"], opts["bot_source"] = owner(opts.get("bot"))

    def _todo(self, job: dict, *args: str) -> None:
        """``worker.todo_link``'s command with *args* on the run's to-do; a failure is one line, never an error."""
        try:
            command = settings.todo_link(self.worker)
            if not command:
                return
            done = subprocess.run([*command, *args], text=True, encoding="utf-8", errors="replace",
                                  capture_output=True, env=kanban_link.hub_env(),
                                  timeout=kanban_link.COMMAND_TIMEOUT_S, check=False)
            if done.returncode != 0:
                raise OSError(f"exited {done.returncode}: {(done.stderr or done.stdout).strip()[-200:]}")
        except (OSError, ValueError, subprocess.TimeoutExpired) as exc:
            self.store.receipt(job["id"], "todo_link_failed", verb=args[0], error=str(exc)[:300])
            self.out(f"lina worker: to-do {job.get('todo')}: {args[0]} for {job['id']} failed: {exc}"[:400])
            return
        self.store.receipt(job["id"], "todo_linked", verb=args[0], todo=job.get("todo"))

    def _submit_one(self, brief: Path, opts: dict, group: str | None, tid: str | None = None) -> dict:
        job = self.make_job(brief, opts, group, tid)
        if opts.get("wait") is not None and not group:
            job.update(self.store.update(job["id"], waiter=os.getpid(), waiter_start=_start_time(os.getpid())) or {})
        if not kanban_link.on_hermes(job.get("card")):
            self._link_card(job)
        try:
            host = self.place(job)
        except (TransportError, HubError, RunnerError, OSError) as exc:
            host = None
            if (self.store.load(job["id"]) or {}).get("state") == "placing":  # the tick resumes it on that device
                self.store.receipt(job["id"], "publish_failed_resumes", error=str(exc)[:300])
            else:
                job.update(self.store.update(job["id"], state="queued") or {})
                self.store.receipt(job["id"], "start_failed_requeued", error=str(exc)[:300])
        self.out(f"{job['id']} handed out · {job['bot']} ({opts['bot_source']}) · topic {job['topic']} · "
                 f"mode {job['mode']} · runner {job['runner']} · model {models.label(job['model'])} · "
                 f"{host or 'queued (starts when a device is free)'}"
                 + (f" · repo {job['repo']} branch {job['branch']}" if job.get("repo") else ""))
        return job

    def _wait(self, task_id: str, timeout: float) -> int:
        deadline = time.time() + timeout
        try:
            while time.time() < deadline:
                time.sleep(min(15.0, max(0.5, timeout / 20)))
                job = self.need(task_id)
                if job.get("host"):
                    remote = self.meta(job)
                    if remote.get("state") in ENDED:
                        out_dir, _ = self.fetch(job)
                        self.out(f"{task_id} {notice.STATE_WORDS.get(remote['state'])} · result {out_dir}")
                        return 0
            self.out(f"{task_id} has not ended yet (--wait ran out). The end notice will come.")
            return 0
        finally:  # every way out (ended, ran out, an error, Ctrl-C): the notice path must not count on this waiter
            self.store.update(task_id, waiter=None, waiter_start=None)


    def schedule(self, opts: dict, sid: str | None = None) -> int:
        when = time.time()
        if opts.get("at"):
            try:
                when = time.mktime(time.strptime(opts["at"], "%Y-%m-%d %H:%M"))
            except ValueError as exc:
                raise HubError('--at must look like "2026-11-01 09:00"') from exc
        every = parse_every(opts["every"]) if opts.get("every") else None
        sid = sid or self.store.new_id(opts["bot"], "OS")
        saved = {k: v for k, v in opts.items() if k not in ("at", "every", "request_key")}
        record = {"id": sid, "bot": opts["bot"], "opts": saved, "next": when, "every": every, "created": time.time()}
        self.store.schedules.mkdir(parents=True, exist_ok=True)
        (self.store.schedules / f"{sid}.json").write_text(json.dumps(record, ensure_ascii=False, indent=1),
                                                         encoding="utf-8")
        self.store.log(f"{sid} scheduled next={time.strftime('%F %T', time.localtime(when))} every={every}")
        self.out(f"{sid} scheduled · first {time.strftime('%m-%d %H:%M', time.localtime(when))}"
                 + (f" · every {opts['every']}" if every else " · once"))
        return 0

    def run_due_schedules(self) -> None:
        if not self.store.schedules.is_dir():
            return
        for path in sorted(self.store.schedules.glob("*.json")):
            record = json.loads(path.read_text(encoding="utf-8-sig"))
            if record.get("off") or record["next"] > time.time():
                continue
            lines: list[str] = []
            # One key per firing: a hub that stopped before saving the next time fires it again as the same request.
            key = f"lina-schedule:{record['id']}:{int(record['next'])}"
            try:
                code = Hub(self.worker_root, out=lines.append).submit(dict(record["opts"], request_key=key))
            except (HubError, RunnerError, OSError) as exc:
                code, lines = 1, [str(exc)]
            record.setdefault("fired", []).append({"at": time.time(), "rc": code, "out": " | ".join(lines)[:200]})
            if record.get("every"):
                while record["next"] <= time.time():
                    record["next"] += record["every"]
            else:
                record["off"] = True
            path.write_text(json.dumps(record, ensure_ascii=False, indent=1), encoding="utf-8")
            self.store.log(f"{record['id']} fired rc={code}")

    def schedules(self) -> int:
        paths = sorted(self.store.schedules.glob("*.json")) if self.store.schedules.is_dir() else []
        if not paths:
            self.out("no schedules")
        for path in paths:
            r = json.loads(path.read_text(encoding="utf-8-sig"))
            when = "ended/off" if r.get("off") else f"next {time.strftime('%m-%d %H:%M', time.localtime(r['next']))}"
            self.out(f"{r['id']}  {r['bot']}  {when}  " + (f"every {r['every'] // 60}m  " if r.get("every") else "")
                     + f"fired {len(r.get('fired', []))}x  {' '.join(r['opts'].get('briefs', []))[:120]}")
        return 0

    def unschedule(self, sid: str) -> int:
        path = self.store.schedules / f"{sid}.json"
        if not path.is_file():
            raise HubError(f"{sid}: no such schedule")
        record = json.loads(path.read_text(encoding="utf-8-sig"))
        record["off"] = True
        path.write_text(json.dumps(record, ensure_ascii=False, indent=1), encoding="utf-8")
        self.out(f"{sid} switched off.")
        return 0


    def meta(self, job: dict, live: bool = False) -> dict:
        reply = self.transport(job).call(["meta", job["id"], *(["live"] if live else [])], timeout=90)
        if reply.code != 0:
            return {"state": "unknown", "unreachable": True, "why": reply.err.strip()[-200:]}
        return reply.json() or {"state": "unknown", "unreachable": True}

    def fetch(self, job: dict) -> tuple[Path, dict]:
        """Copy the results to the hub and check them against the device's manifest."""
        out_dir = self.store.out_dir(job)
        fetch(self.transport(job), job["id"], out_dir)
        check = verify(out_dir)
        state = "missing" if "error" in check else "ok" if check["ok"] and check.get("inputs_unchanged") is not False \
            else "mismatch"
        self.store.update(job["id"], delivery=state, delivery_check=check)
        self.store.receipt(job["id"], "delivery", state=state, **{k: v for k, v in check.items() if k != "ok"})
        return out_dir, check

    def result(self, task_id: str) -> int:
        job = self.need(task_id)
        if not job.get("host"):
            raise HubError(f"{task_id} has not started yet")
        out_dir, check = self.fetch(job)
        for name in ("report.md", "final.md", "git.txt", "manifest.json"):
            if (out_dir / name).is_file():
                self.out(str(out_dir / name))
        if (out_dir / "out").is_dir():
            self.out(f"{out_dir / 'out'}/ ({sum(1 for p in (out_dir / 'out').rglob('*') if p.is_file())} files)")
        self.out(f"delivery check: {notice.DELIVERY_WORDS[self.need(task_id)['delivery']]}"
                 + (f" · differ {check['mismatch']}" if check.get("mismatch") else "")
                 + (f" · missing {check['missing']}" if check.get("missing") else "")
                 + (" · the input copies changed" if check.get("inputs_unchanged") is False else ""))
        return 0


    def status(self, task_id: str, as_json: bool = False) -> int:
        job = self.need(task_id)
        access = job.get("run_access", {}).get(str(job.get("runs", 1)), job.get("access", "workspace-write"))
        head = (f"{job['id']} · {job['bot']} · {job['topic']} · mode {job.get('mode')} · runner {job.get('runner')} · "
                f"model {models.label(job.get('model'))} · access {access}")
        if not job.get("host"):
            state = job.get("state", "queued")
            self.out(f"{head} · {notice.STATE_WORDS.get(state, state)} · handed out {_ago(job['created'])} ago"
                     + (" · starts when a device is free (`lina worker hosts`)" if state == "queued" else ""))
            return 0
        remote = self.meta(job, live=True)
        if as_json:
            self.out(json.dumps({**{k: v for k, v in job.items() if k != "prompt"}, "remote": remote},
                                ensure_ascii=False))
            return 0
        state = remote.get("state") or "unknown"
        if state == "running" and not remote.get("alive") and not remote.get("unreachable"):
            state = "unknown" if job.get("state") == "unknown" else state
        self.out(f"{head} · {job['host']} · {notice.STATE_WORDS.get(state, state)} · run {remote.get('run')} · "
                 f"handed out {_ago(job['created'])} ago" + (f" · rc {remote['rc']}" if remote.get("rc") else ""))
        live = remote.get("live") or {}
        if state == "running" and live:
            self.out(f"  now: {live.get('dash') or 'running'}" + (f" · {live['why']}" if live.get("why") else "")
                     + (f" (omo {live['lifecycle']})" if live.get("lifecycle") else "")
                     + (f" · recent tools {', '.join(t for t in live.get('tools') or [] if t)}" if live.get("tools")
                        else ""))
            if live.get("last"):
                self.out(f"  last said: {live['last'][:240]}")
        elif state == "review":
            self.out(f"  needs review: the '{NEXT_NOTES}' section of report.md is missing or empty. "
                     f"`lina worker send {job['id']} \"fill that section\"`, or copy it yourself and accept.")
        elif remote.get("final"):
            self.out(f"  last said: {remote['final'][:240].strip()}")
        if job.get("repo"):
            self.out(f"  repo {job['repo']} · branch {job['branch']} (push blocked)")
        self.out(f"  delivery: {notice.DELIVERY_WORDS.get(job.get('delivery', 'pending'))} · verdict: "
                 f"{VERDICT_WORDS.get(job.get('verdict'))}" + (f" · group {job['group']}" if job.get("group") else ""))
        return 0

    def list(self, bot: str | None = None, show_all: bool = False) -> int:
        for job in reversed(self.store.all()):
            state = job.get("state") or "running"
            if (bot and job["bot"] != bot) or (not show_all and time.time() - job["created"] > 86400 and state in ENDED):
                continue
            self.out(f"{job['id']}  {job['bot']}  {job['topic']}  {job.get('mode')}  {job.get('runner')}  "
                     f"{models.label(job.get('model'))}  {job.get('host') or '-'}  {notice.STATE_WORDS.get(state, state)}"
                     f"  {notice.DELIVERY_WORDS.get(job.get('delivery', 'pending'))}  "
                     f"{VERDICT_WORDS.get(job.get('verdict'))}  {time.strftime('%m-%d %H:%M', time.localtime(job['created']))}"
                     + (f"  {job['group']}" if job.get("group") else ""))
        return 0

    def receipts(self, task_id: str) -> int:
        for record in self.store.receipts_of(task_id):
            extra = {k: v for k, v in record.items() if k not in ("at", "ts", "task", "kind")}
            self.out(f"{record['at']}  {record['kind']:<16} {json.dumps(extra, ensure_ascii=False, default=str)[:220]}")
        return 0

    def hosts(self, runner: str | None = None) -> int:
        _, table = bidding.bid(self.devices(), self.worker_root, repo=None,
                               runner=runner or runners.default_name(self.worker), manual=True,
                               min_free_gb=int(settings.setting("min_free_gb", worker=self.worker)))
        if not table:
            self.out("no devices: add them under devices.workers in config.yaml")
        for row in table:
            self.out(f"{row['host']:<14} cap {row['cap']} · running {row.get('running', '-')} · load "
                     f"{row.get('load', '-')}/{row.get('cores', '-')} cores · free disk {row.get('free_gb', '-')}GB · "
                     f"runner {row.get('runner', '-')}" + (f" · {row['why']}" if row.get("why") else " · can take work"))
        queued = [job["id"] for job in self.store.all() if job.get("state") == "queued"]
        self.out(f"queue: {len(queued)}" + (f" ({', '.join(queued)})" if queued else ""))
        return 0


    def send(self, task_id: str, message: str, card: dict | None = None, *, access: str | None = None,
             request_key: str | None = None) -> int:
        if request_key:
            return request_keys.send(self, task_id, message, card, access, request_key)
        # Keep access decisions and device requests in the same order across concurrent senders.
        with self.store.lock(f"send-{task_id}"):
            return self._send_locked(task_id, message, card, access=access)

    def _send_locked(self, task_id: str, message: str, card: dict | None, *, access: str | None,
                     request_key: str | None = None) -> int:
        job = self.need(task_id)
        card = card or kanban_link.card_from_env()
        if card and any((job.get("card") or {}).get(key) != card.get(key) for key in ("task", "board")):
            raise HubError(f"{task_id}: run does not belong to card {card['task']} on board {card.get('board')}")
        if not job.get("host"):
            raise HubError(f"{task_id} has not started yet (queued): cancel it and submit a new brief instead")
        if job.get("state") == "placing":
            raise HubError(f"{task_id} is still being handed to {job['host']}: send it again in a moment")
        recorded = job.get("access", "workspace-write")
        requested = recorded if access is None else access
        if requested not in ACCESS or recorded not in ACCESS:
            raise HubError(f"invalid access mode: {requested!r}")
        if ACCESS.index(requested) > ACCESS.index(recorded):
            raise HubError(f"access {requested} exceeds recorded access {recorded}; submit a new task")
        transport, remote = self.transport(job), self.meta(job)
        human = transport.call(["human", task_id], timeout=90).json()
        if human.get("permission"):
            raise HubError(f"{task_id}: permission_dialog")
        if requested != recorded:
            return self._next_run(job, message, access=requested, request_key=request_key)
        if human.get("open"):
            reply = self._device_write(job, transport, "answer", {"message": message}, request_key)
            return self._answered(job, reply, remote, message, request_key)
        if remote.get("alive"):
            reply = self._device_write(job, transport, "send", {"message": message}, request_key)
            return self._steered(job, reply, message, request_key)
        return self._next_run(job, message, request_key=request_key)

    def resend(self, task_id: str, record: dict) -> int:
        """A keyed send whose device reply was lost: the verb it chose, with the same arguments and key, never a new
        route (the task's state may have moved on, and another verb would apply the words a second time)."""
        job, verb, args, key = self.need(task_id), record["device_verb"], record["device_args"], record["key"]
        reply = self._device_write(job, self.transport(job), verb, args, key, record["device_phase"])
        if verb == "answer":
            return self._answered(job, reply, self.meta(job), args["message"], key)
        if verb == "send":
            return self._steered(job, reply, args["message"], key)
        return self._followed(job, reply, args, args["access"], key)

    def _answered(self, job: dict, reply: dict, remote: dict, message: str, request_key: str | None) -> int:
        task_id = job["id"]
        if (reply.get("reason") or "").split(":", 1)[0] == "permission_dialog":
            raise HubError(f"{task_id}: permission_dialog")
        run = reply.get("run") or remote.get("run") or job.get("runs", 1)
        answered = reply.get("action") == "answer" and reply.get("steps") and not reply.get("still_pending")
        # A replayed answer that left its question open goes on to the next run only while its run is the task's.
        if reply.get("replayed") and (answered or int(run) < int(job.get("runs") or 1)):
            return request_keys.reconcile(self, task_id, request_key, {**reply, "run": run})
        self.store.receipt(task_id, "answer", run=run, response={k: reply.get(k) for k in (
            "action", "steps", "still_pending", "reason")})
        if answered:
            request_keys.sent(self.store, task_id, request_key, "answer", run)
            self.out(NEXT_RUN_WORDS["answer"].format(id=task_id, run=run))
            return 0
        if not self._closed(job, int(run)):
            raise HubError(f"{task_id} run {run} did not close on its question; send again in a moment")
        # The device keeps the answer's reply under its key: the next run is its own device request.
        return self._next_run(job, "Answer to the earlier question: " + message, request_key=request_key,
                              phase="next")

    def _steered(self, job: dict, reply: dict, message: str, request_key: str | None) -> int:
        task_id = job["id"]
        if reply.get("replayed") and reply.get("action") in ("steered", "started"):
            return request_keys.reconcile(self, task_id, request_key, reply)
        self.store.receipt(task_id, "steer", run=reply.get("run"), response=reply)
        if reply.get("action") in ("steered", "started"):
            request_keys.sent(self.store, task_id, request_key, reply["action"], reply.get("run"))
            self.out(NEXT_RUN_WORDS["steered"].format(id=task_id, run=reply.get("run")))
            return 0
        if reply.get("action") == "refused":
            raise HubError(f"{task_id}: runner {job.get('runner')} has no channel into a running turn; send it "
                           "again when this run ends")
        if reply.get("action") == "error":
            raise HubError(f"steer failed: {reply.get('reason')}")
        return self._next_run(job, message, request_key=request_key)

    def _closed(self, job: dict, run: int) -> bool:
        """After an answer closed run *run* on its question: wait for its end, recorded here once and silently (the
        person answering is the one the notice would go to; the next run's end notice follows)."""
        deadline = time.time() + ANSWER_CLOSE_WAIT_S
        while self.meta(job).get("rc") in (None, "") and self.meta(job).get("alive"):
            if time.time() > deadline:
                return False
            time.sleep(2)
        marker = f"stalled:{run}"
        with self.store.edit(job["id"]) as job:
            first = marker not in job.get("notified", [])
            if first:
                job.setdefault("notified", []).append(marker)
        if first:
            self.store.receipt(job["id"], "exec_end", state="stalled", run=str(run), why="closed on an open question "
                                                                                        "by send")
        return True

    def _next_run(self, job: dict, message: str, *, access: str | None = None, request_key: str | None = None,
                  phase: str = "req") -> int:
        """Ask the device for the next run; it serializes this against a run still waiting, starting or running."""
        payload = {"prompt": follow_up_prompt(job.get("mode") or "ulw", message), "message": message,
                   "access": job.get("access", "workspace-write") if access is None else access}
        reply = self._device_write(job, self.transport(job), "follow_up", payload, request_key, phase)
        return self._followed(job, reply, payload, access, request_key)

    def _followed(self, job: dict, reply: dict, payload: dict, access: str | None, request_key: str | None) -> int:
        action, run = reply.get("action"), reply.get("run")
        if action in ("busy", "refused"):
            raise HubError(f"{job['id']} run {run} is starting or running and cannot take an instruction now; send it "
                           "again when it is live or has ended")
        if action not in NEXT_RUN_WORDS:
            raise HubError(f"follow-up failed: {reply.get('reason') or reply}")
        if reply.get("replayed"):
            return request_keys.reconcile(self, job["id"], request_key, reply, access=access,
                                          run_access=payload["access"])
        with self.store.edit(job["id"]) as stored:
            request_keys.lower_access(stored, access)
            if action == "queued":
                stored.setdefault("run_access", {})[str(run)] = payload["access"]
            if action == "requested":
                request_keys.take_run(stored, run, payload["access"])
        if action in ("answer", "steered", "started"):
            self.store.receipt(job["id"], "answer" if action == "answer" else "steer", run=run, response=reply)
        else:
            self.store.receipt(job["id"], "follow_up", input_id=f"{job['id']}.{run}", run=run,
                               **({"queued": True} if action == "queued" else {}))
        request_keys.sent(self.store, job["id"], request_key, action, run)
        self.out(NEXT_RUN_WORDS[action].format(id=job["id"], run=run))
        return 0

    def _device_write(self, job: dict, transport, verb: str, args: dict, request_key: str | None,
                      phase: str = "req") -> dict:
        """A device verb that changes the task (``answer``, ``send``, ``follow_up``), carrying the device key of
        *phase*. With a request key the verb and its arguments go on the key's record first, so a retry sends that
        same verb again. A failed call with no reply is an error, never read as the device's word."""
        key = request_keys.device_key(request_key, phase) if request_key else None
        if request_key:
            request_keys.calling(self.store, request_key, verb, args, phase)
        if verb == "follow_up":
            argv, data = ["follow_up", job["id"]], json.dumps({**args, **({"request_key": key} if key else {})})
        else:
            argv, data = [verb, job["id"], *([key] if key else [])], args["message"]
        answer = transport.call(argv, data.encode("utf-8"), timeout=180)
        reply = answer.json()
        if answer.code != 0 and not reply:
            raise HubError(f"{job['id']}: {verb} got no reply from {job.get('host')} ("
                           f"{answer.err.strip()[-200:] or f'exit {answer.code}'}); send it again"
                           + (f" with request key {request_key}" if request_key else ""))
        if request_key:
            request_keys.note_device(self.store, job, transport)
        return reply

    def cancel(self, task_id: str, why: str = "") -> int:
        job = self.need(task_id)
        why = (why or "").replace("\n", " ")[:300]
        job = self.store.update(task_id, cancel_why=why) or job
        self.store.receipt(task_id, "cancel_requested", why=why)
        if not job.get("host"):
            self.notify(task_id, "cancelled", "0")
            self.out(f"{task_id} cancelled while queued.")
            return 0
        answer = self.transport(job).call(["cancel", task_id], (why or "cancelled").encode("utf-8"), timeout=120)
        reply = answer.json()
        if answer.code != 0 or reply.get("action") != "cancel":
            raise HubError(f"{task_id}: the cancel did not reach {job['host']} (rc {answer.code}: "
                           f"{(answer.err.strip() or reply.get('reason') or 'no acknowledgement')[-300:]}); the task "
                           "is not cancelled, try again")
        self.out(f"{task_id} cancelled (the runner was stopped). The task folder stays. One notice follows.")
        if not reply.get("alive"):
            self.notify(task_id, "cancelled", str(reply.get("run") or 1))
        return 0

    def verdict(self, task_id: str, accepted: bool, note: str = "") -> int:
        job = self.need(task_id)
        verdict = "accepted" if accepted else "rejected"
        job = self.store.update(task_id, verdict=verdict, verdict_note=note) or job
        self.store.receipt(task_id, "verdict", verdict=verdict, note=note[:500], by=owner()[0],
                           delivery=job.get("delivery"))
        self.out(f"{task_id} {verdict}")
        if accepted and job.get("todo"):
            self._todo(job, "evidence", job["todo"], f"omo:{task_id}")
            if note:
                self._todo(job, "note", job["todo"], f"{task_id} checked: {note}")
        return 0

    def card(self, task_id: str, card: str | None = None, drop: str | None = None) -> int:
        """The card a run was handed out from; a run's card is set at submit and never changed afterwards."""
        job = self.need(task_id)
        if card or drop:
            raise HubError(f"{task_id}: a run's card is set at submit (--card); it cannot be added or dropped")
        linked = job.get("card")
        self.out(f"{linked['task']}  {linked.get('board') or '-'}  ({linked.get('kind') or 'lina'})" if linked
                 else f"{task_id}: no card")
        return 0


    def notify(self, task_id: str, kind: str, run: str) -> str:
        """Deliver the end of run *run* once, whoever reports it first (the device or the tick)."""
        with self.store.lock(task_id):
            job = self.store.load(task_id)
            if not job:
                self.store.log(f"{task_id} {kind}: no job record, skipped")
                return "no job"
            return self._notify_locked(job, kind, run)

    def _adopt(self, job: dict, run: int) -> None:
        """An end reported for a run newer than the job's: the hub missed the reply of the follow-up that started it
        (an ssh drop, keyed or not), so the run is the task's now and its end is delivered as usual."""
        held, remote = int(job.get("runs") or 1), self.meta(job)
        # The device job holds the access its latest run was requested with.
        request_keys.take_run(job, run, remote.get("access") if str(remote.get("run")) == str(run) else None)
        self.store.save(job)
        self.store.receipt(job["id"], "run_adopted", run=run, **{"from": held})

    def _waiter_alive(self, job: dict) -> bool:
        """The ``submit --wait`` process is still the one that registered: ``(pid, start time)``, never a bare
        PID (a reused PID would swallow the end notice with nobody left to show it)."""
        pid = int(job.get("waiter") or 0)
        start = job.get("waiter_start")
        if not pid or start is None:
            return False
        return _start_time(pid) == start

    def _notice_body(self, job: dict, kind: str, run: str) -> tuple[str, Path]:
        """The end notice and where the results are; ``self.fetched`` says whether fetching them worked (None:
        nothing to fetch)."""
        out_dir, check = self.store.out_dir(job), {}
        if job.get("host") and kind != "unknown":
            try:
                out_dir, check = self.fetch(job)
                self.fetched = True
            except (TransportError, HubError) as exc:
                self.fetched = False
                self.store.receipt(job["id"], "fetch_failed", error=str(exc)[:300])
            job.update(self.store.load(job["id"]) or {})
        manifest = read_json(out_dir / "manifest.json", {}) or {}
        err_tail = (out_dir / "stderr.txt").read_text(encoding="utf-8-sig", errors="replace")[-1500:] \
            if (out_dir / "stderr.txt").is_file() else ""
        text = notice.body(job, kind, run, out_dir, check, notes=manifest.get("next_notes"),
                           quiet_s=float((manifest.get("wait") or {}).get("quiet_s") or 0), err_tail=err_tail,
                           unknown_after_s=int(settings.setting("unknown_after_s", worker=self.worker)))
        if job.get("extra"):
            text = f"[note left at hand-out] {job['extra']}\n\n" + text
        return text, out_dir

    def _notify_locked(self, job: dict, kind: str, run: str) -> str:
        if job.get("card_start_pending"):
            self._link_card(job)
        marker = f"{kind}:{run}"
        if marker in job.get("notified", []):
            return self._card_steps(job, marker) if marker in (job.get("card_pending") or {}) else "already"
        if str(run).isdigit() and int(run) > int(job.get("runs") or 1):
            self._adopt(job, int(run))
        elif str(run).isdigit() and int(run) >= 1 and int(run) < int(job.get("runs") or 1):
            # A late end of an earlier run (its notice retried after a follow-up started the next one): record it,
            # never let it end the run that is going now. Task-level notices are not run reports and always apply:
            # a cancel before any run starts ("0") and an unreachable device ("x").
            job.setdefault("notified", []).append(marker)
            self.store.save(job)
            self.store.receipt(job["id"], "exec_end", state=kind, run=run, stale=True)
            return "earlier run"
        job.update(state=kind, last_state=kind)
        self.store.receipt(job["id"], "exec_end", state=kind, run=run)
        if self._waiter_alive(job):
            job.setdefault("notified", []).append(marker)
            self.store.save(job)
            return "waiter"
        self.store.save(job)
        self.fetched = None
        text, _ = self._notice_body(job, kind, run)
        fetched = self.fetched
        job.setdefault("notified", []).append(marker)
        job["notice"] = text
        self.store.save(job)
        if kanban_link.on_hermes(job.get("card")):  # the card is the one place the run ends: no chat notice
            how = self._card_end(job, kind, run, text, fetched)
        else:
            how = self.group_notice(job["group"]) if job.get("group") else self._deliver(job, text, f"{kind}-{run}")
            if job.get("card"):
                self._card_end(job, kind, run, text)
        self.store.receipt(job["id"], "notified", how=how[:200], state=kind, run=run)
        self.store.log(f"{job['id']} {kind}: {how}")
        return how

    def _deliver(self, job: dict, text: str, tag: str) -> str:
        command = (self.worker.get("notice") or {}).get("command") if isinstance(self.worker.get("notice"), dict) \
            else None
        return notice.deliver(job, text, tag, self.store.notices, command)

    def _card_end(self, job: dict, kind: str, run: str, text: str, fetched: bool | None = None) -> str:
        """A run's end on its card. On a Hermes card it becomes pending work (``card_pending``): its result fetch
        when that failed, then ``kanban_link.END_STEPS``, with this run's own report.md kept aside for the attachment."""
        card = job["card"]
        if kanban_link.on_hermes(card):
            marker = f"{kind}:{run}"
            job.setdefault("card_pending", {})[marker] = {
                "kind": kind, "run": run, "text": text, "done": [], "fetch_pending": fetched is False,
                "fetch_tries": 1 if fetched is False else 0,
                "report": None if fetched is False else self._report_snapshot(job, marker)}
            self.store.save(job)
            if fetched is False:
                return f"card {card['task']}: result not fetched yet (1/{FETCH_TRIES}), retried on the next report or tick"
            return self._card_steps(job, marker)
        try:
            kanban_link.record_end(card, job, kind, run, text)
        except (ValueError, OSError) as exc:
            self.store.receipt(job["id"], "card_notice_failed", error=str(exc)[:300])
            return f"card {card['task']}: failed ({str(exc)[:120]})"
        self.store.receipt(job["id"], "card_noticed", card=card, state=kind, run=run, woke=False)
        return f"card {card['task']}"

    def _card_steps(self, job: dict, marker: str) -> str:
        """Carry the end *marker* of a Hermes card run through the steps it has not done yet, saving each one that
        went through: a step that fails stays pending for the next report or tick, and none is done twice. Called
        under the job's lock."""
        card, link = job["card"], self._link()
        if job.get("card_start_pending") or "HERMES_DELEGATED_CHILD_CONTEXT" in os.environ:
            return f"card {card['task']}: pending hub tick"
        if job["card_pending"][marker].get("fetch_pending") and not self._card_fetch(job, marker):
            return (f"card {card['task']}: result not fetched yet ({job['card_pending'][marker]['fetch_tries']}/"
                    f"{FETCH_TRIES}), retried on the next report or tick")
        entry = job["card_pending"][marker]
        for step in kanban_link.END_STEPS:
            if step in entry["done"]:
                continue
            try:
                woke = kanban_link.end_step(step, self.store, card, job, entry["run"], entry["text"], link,
                                            Path(entry["report"]) if entry.get("report") else None)
            except (OSError, kanban_link.CardLinkError) as exc:
                self.store.receipt(job["id"], "card_notice_failed", step=step, error=str(exc)[:300])
                return f"card {card['task']}: {step} failed, retried on the next report or tick"
            entry["done"].append(step)
            entry["woke"] = entry.get("woke") or woke
            self.store.save(job)
        del job["card_pending"][marker]
        self.store.save(job)
        if entry.get("report"):
            shutil.rmtree(Path(entry["report"]).parent, ignore_errors=True)
        self.store.receipt(job["id"], "card_noticed", card=card, state=entry["kind"], run=entry["run"],
                           woke=bool(entry.get("woke")))
        return f"card {card['task']}" + (" (woken)" if entry.get("woke") else "")

    def _report_snapshot(self, job: dict, marker: str) -> str | None:
        """A copy of the run's report.md as fetched now: a later run's fetch replaces the one in out/."""
        report = self.store.out_dir(job) / "report.md"
        if not report.is_file():
            return None
        kept = self.store.root / "card_reports" / f"{job['id']}-{marker.replace(':', '-')}" / "report.md"
        kept.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(report, kept)
        return str(kept)

    def _card_fetch(self, job: dict, marker: str) -> bool:
        """Fetch the results of a pending end again; True once the end can go on (fetched, or out of tries: then
        without report.md, saying so). The device keeps only its latest run's results: when the manifest names a later
        run, they are not this run's, and the end goes on without them."""
        kind, run = job["card_pending"][marker]["kind"], job["card_pending"][marker]["run"]
        self.fetched = None
        text, out_dir = self._notice_body(job, kind, run)  # reloads the job: read the entry after it
        entry = job["card_pending"][marker]
        fetched_run = (read_json(out_dir / "manifest.json", {}) or {}).get("run")
        if self.fetched and fetched_run is not None and str(fetched_run) != str(run):
            entry["fetch_pending"] = False
            entry["text"] += (f"- Run {run}'s results were replaced by a later run before they could be fetched, so no "
                              "report is attached.\n")
        elif self.fetched:
            entry.update(text=text, fetch_pending=False, report=self._report_snapshot(job, marker))
        else:
            entry["fetch_tries"] += 1
            if entry["fetch_tries"] >= FETCH_TRIES:
                entry["fetch_pending"] = False
                entry["text"] += (f"- report.md could not be fetched from {job.get('host')} after {FETCH_TRIES} tries: "
                                  f"`lina worker result {job['id']}` fetches it again.\n")
        self.store.save(job)
        return not entry["fetch_pending"]

    def _retry_cards(self, task_id: str) -> None:
        with self.store.lock(task_id):
            job = self.store.load(task_id) or {}
            if job.get("card_start_pending"):
                self._link_card(job)
            for marker in list(job.get("card_pending") or {}):
                self.store.log(f"{task_id} card retry {marker}: {self._card_steps(job, marker)}")

    def wait(self, card: dict | None, task_ids: list[str] | None = None) -> int:
        """Record open runs and tell the card's model how to schedule itself. The hub wakes it after a run ends."""
        link = self._link()
        if link["kind"] != kanban_link.HERMES:
            raise HubError("wait parks a Hermes card: set worker.card_link {kind: hermes, command: [...]}")
        card = card or kanban_link.card_from_env()
        if not card:
            raise HubError("wait: no card (--card <id> --board <slug>, or run it in the card's session)")
        card = kanban_link.card_for(card, link)
        with self.store.lock(f"card-{kanban_link.card_key(card)}"):
            open_runs = [job["id"] for job in self.store.all()
                         if (job.get("card") or {}).get("task") == card["task"]
                         and (job.get("card") or {}).get("board") == card.get("board")
                         and job.get("state") not in ENDED + ("unknown",) and (not task_ids or job["id"] in task_ids)]
            if not open_runs:
                raise HubError(f"card {card['task']} has no open run to wait for; read the ends on the card "
                               "(`lina worker status <ID>`, `lina worker result <ID>`)")
            kanban_link.park(self.store, card, open_runs)
        self.out(f"Call Hermes kanban_schedule with workers={json.dumps(open_runs)}, "
                 f"reason=\"waiting on lina worker {' '.join(open_runs)}\", then end the turn.")
        return 0

    def group_notice(self, group: str) -> str:
        members = [job for job in self.store.all() if job.get("group") == group]
        open_ = [job for job in members if job.get("state") not in ENDED + ("unknown",)]
        if open_:
            return f"group {group}: waiting for {len(open_)} more"
        marker = self.store.locks / f"{group}.sent"
        if marker.exists():
            return f"group {group}: already sent"
        first = members[0]
        lines = [f"[lina worker · automatic] {first['bot']} · batch {group} ({len(members)} tasks, "
                 f"{first.get('todo') or first['topic']}) all ended"]
        lines += [f"- {j['id']} {notice.STATE_WORDS.get(j['state'], j['state'])} · delivery "
                  f"{notice.DELIVERY_WORDS.get(j.get('delivery', 'pending'))} · brief {Path(j['brief']).name} · result "
                  f"{self.store.out_dir(j)}" for j in members]
        lines += ["To do:", "1. For each task hold report.md and out/ against the done conditions, then "
                  "`lina worker accept`/`reject`; what is missing goes to `lina worker send <ID> …`.",
                  f"2. Copy each report's '{NEXT_NOTES}' section into the notes of that field."]
        how = self._deliver(first, "\n".join(lines) + "\n", f"group-{group}")
        marker.parent.mkdir(parents=True, exist_ok=True)
        marker.write_text(how, encoding="utf-8")
        self.store.receipt(group, "group_notified", how=how[:200], members=[j["id"] for j in members], group=group)
        return f"group {group}: {how}"


    def tick(self) -> None:
        with self.store.lock("tick", wait=False) as held:
            if not held:
                return
            self.run_due_schedules()
            for job in self.store.all():
                if job.get("card_start_pending") or job.get("card_pending"):
                    self._retry_cards(job["id"])
            for job in self.store.all():
                if job.get("state") == "queued":
                    if not self._try_place(job):
                        break  # first in, first out: nothing free for the oldest means nothing for newer ones
                elif job.get("state") == "placing":
                    self._try_resume(job)
                elif job.get("state") == "running" and job.get("host") and time.time() - job["created"] < 7 * 86400:
                    self._watch(job)

    def _try_place(self, job: dict) -> bool:
        try:
            return self.place(job) is not None
        except (TransportError, HubError, RunnerError, OSError) as exc:
            self.store.log(f"{job['id']} start failed on tick: {exc}")
            return False

    def _try_resume(self, job: dict) -> None:
        try:
            self.resume_placement(job["id"])
        except (TransportError, HubError, OSError) as exc:
            self.store.log(f"{job['id']} placement not resumed on tick: {exc}")

    def _watch(self, job: dict) -> None:
        now, remote = time.time(), self.meta(job)
        if remote.get("unreachable") or remote.get("missing"):
            with self.store.edit(job["id"]) as job:
                job.setdefault("unreachable_since", now)
            if now - job["unreachable_since"] >= int(settings.setting("unknown_after_s", worker=self.worker)):
                self.notify(job["id"], "unknown", "x")
            return
        if job.pop("unreachable_since", None):
            with self.store.edit(job["id"]) as stored:
                stored.pop("unreachable_since", None)
        state, run = remote.get("state"), str(remote.get("run"))
        if state in ENDED and f"{state}:{run}" not in job.get("notified", []):
            self.notify(job["id"], state, run)
        elif state == "running" and (remote.get("live") or {}).get("dash") == "human":
            self._human_notice(job, run, now)
        elif job.pop("human_since", None):
            with self.store.edit(job["id"]) as stored:
                stored.pop("human_since", None)

    def _human_notice(self, job: dict, run: str, now: float) -> None:
        with self.store.edit(job["id"]) as job:
            self._human_notice_locked(job, run, now)

    def _human_notice_locked(self, job: dict, run: str, now: float) -> None:
        try:  # a permission dialog is answered by the runner, never a person; an unknown answer still notifies
            human = self.transport(job).call(["human", job["id"]], timeout=90).json()
        except (TransportError, HubError, OSError):
            human = {}
        if human.get("permission"):
            job.pop("human_since", None)
            return
        job.setdefault("human_since", now)
        if now - job["human_since"] < HUMAN_NOTICE_AFTER_S:
            return
        if kanban_link.on_hermes(job.get("card")):
            self._card_question(job, run, human)
            return
        marker = f"human:{run}"
        if marker not in job.get("notified", []):
            job.setdefault("notified", []).append(marker)
            text = notice.body(job, "human", run, self.store.out_dir(job), {})
            how = self._deliver(job, text, f"human-{run}")
            if job.get("card"):
                self._card_end(job, "human", run, text)
            self.store.receipt(job["id"], "notified", how=how[:200], state="human", run=run)

    def _card_question(self, job: dict, run: str, human: dict) -> None:
        """One comment on the run's Hermes card for each question it waits on, with the question; a comment that
        did not go through is tried again on the next tick. Called under the job's lock."""
        text, key = (human.get("question") or "").strip(), ""
        if not text:  # the omo runner's questions come with its live view
            questions = [q for q in (self.meta(job, live=True).get("live") or {}).get("questions") or []
                         if isinstance(q, dict)]
            text = _question_text(questions)
            key = ",".join(str(q["id"]) for q in questions if q.get("id"))
        key = key or (hashlib.sha256(text.encode("utf-8")).hexdigest()[:12] if text else "-")
        marker = f"human:{run}:{key}"
        if marker in job.get("notified", []) or job.get("card_start_pending") or \
                "HERMES_DELEGATED_CHILD_CONTEXT" in os.environ:
            return
        body = (f"[lina worker · automatic] run {job['id']} (run {run}) waits for an answer:\n\n"
                + (text or "(the question did not come with the run's state: `lina worker status "
                           f"{job['id']}` shows it)")
                + f"\n\nAnswer with `lina worker send {job['id']} \"<answer>\"`; to stop: "
                  f"`lina worker cancel {job['id']} --why …`.")
        try:
            kanban_link.comment(self._link()["command"], job["card"], body)
        except (OSError, kanban_link.CardLinkError) as exc:
            self.store.receipt(job["id"], "card_notice_failed", step="question", error=str(exc)[:300])
            return
        job.setdefault("notified", []).append(marker)
        self.store.receipt(job["id"], "notified", how=f"card {job['card']['task']}: question", state="human",
                           run=run)


def _private_lines(paths: list[str]) -> str:
    if not paths:
        return ""
    return ("\nThe owner's private files, to read where they are (never copy them into this folder, out/ or "
            "report.md, and never change them):\n" + "".join(f"- {path}\n" for path in paths))


def parse_every(value: str) -> int:
    match = re.fullmatch(r"(\d+)([mhd])", value or "")
    if not match:
        raise HubError(f"--every must look like 30m, 6h, 1d: {value}")
    return int(match.group(1)) * {"m": 60, "h": 3600, "d": 86400}[match.group(2)]


def notify(worker_root: Path, task_id: str, kind: str, run: str) -> str:
    return Hub(worker_root).notify(task_id, kind, run)


def tick(worker_root: Path) -> None:
    Hub(worker_root).tick()

