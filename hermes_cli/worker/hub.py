"""The hub side of ``lina worker``: submit, send, status, result, accept, reject, cancel, list, receipts, hosts,
schedules, the tick and the end notice.

A task goes to the device that wins the bid (or the one named with ``--host``), or waits in the queue until one is
free. Its first prompt carries the mode prefix and the report contract; a run started from a work card is recorded on
that card. When a run ends the device reports it (``_notify``); the hub fetches the results, checks them against the
device's manifest and sends one notice. ``tick`` (run by the service on the hub) catches what a device could not
report.
"""

from __future__ import annotations

import json
import os
import re
import time
from pathlib import Path

from hermes_cli.worker import bidding, kanban_link, models, notice, runners, settings
from hermes_cli.worker.hub_store import Store
from hermes_cli.worker.report import NEXT_NOTES, first_prompt, follow_up_prompt
from hermes_cli.worker.runners.base import RunnerError
from hermes_cli.worker.task import read_json, verify
from hermes_cli.worker.transport import TransportError, fetch, for_device, put, put_tree

ENDED = ("done", "done_empty", "review", "stalled", "failed", "cancelled")
VERDICT_WORDS = {None: "no verdict yet", "accepted": "accepted", "rejected": "rejected"}
HUMAN_NOTICE_AFTER_S = 120
ANSWER_CLOSE_WAIT_S = 150


def _start_time(pid: int) -> int | None:
    """The process's start fingerprint (the repo's canonical one); ``None`` when it is gone or unreadable."""
    from gateway.status import get_process_start_time
    return get_process_start_time(pid)


class HubError(RuntimeError):
    pass


def caller_agent() -> str:
    from hermes_cli.profiles import get_active_profile_name
    return get_active_profile_name() or "default"


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


    def make_job(self, brief: Path, opts: dict, group: str | None) -> dict:
        agent = opts["bot"]
        tid = self.store.new_id(agent)
        branch = f"omo/{tid}" if opts.get("repo") else None
        runner = opts.get("runner") or runners.default_name(self.worker)
        spec = runners.specs(self.worker).get(runner)
        if spec is None:
            raise HubError(f"no runner {runner!r} (configured: {', '.join(runners.specs(self.worker))})")
        model = models.for_run(opts["mode"], opts.get("model"), self.worker)
        grant = {"workspace": "work/ of the task folder", "inputs_readonly": True, "push": False,
                 "repo": opts.get("repo"), "outside_effects": "report only (messages, publish, delete, money, "
                                                              "credentials need the owner)"}
        job = {"id": tid, "bot": agent, "topic": opts["topic"], "host": None, "fixed_host": opts.get("host"),
               "repo": opts.get("repo"), "branch": branch, "mode": opts["mode"], "brief": str(brief),
               "todo": opts.get("todo"), "runner": runner, "runner_spec": spec,
               "model": model.as_dict() if model else None, "created": time.time(), "runs": 1, "notified": [],
               "extra": opts.get("note"), "dry_notify": bool(opts.get("dry_notify")), "inputs": opts["inputs"],
               "prompt": first_prompt(opts["mode"], brief.read_text(encoding="utf-8-sig"), branch), "state": "new",
               "grant": grant, "delivery": "pending", "verdict": None, "group": group, "next_notes_rule": True,
               "card": opts.get("card")}
        self.store.save(job)
        self.store.receipt(tid, "submitted", bot=agent, topic=job["topic"], mode=job["mode"], runner=runner,
                           model=job["model"], brief=str(brief), repo=job["repo"], group=group, grant=grant,
                           card=job["card"], input_id=f"{tid}.1")
        return job

    def _link_card(self, job: dict) -> None:
        if not job.get("card"):
            return
        try:
            kanban_link.record_run(job["card"], job)
        except (ValueError, OSError) as exc:
            self.store.receipt(job["id"], "card_link_failed", error=str(exc)[:300])
            self.out(f"lina worker: could not record {job['id']} on card {job['card']['task']}: {exc}")
            return
        self.store.receipt(job["id"], "card_linked", card=job["card"])

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
        device_job = {key: job.get(key) for key in ("id", "mode", "runner", "runner_spec", "model", "repo", "branch",
                                                     "next_notes_rule")}
        device_job["notify"] = self._notify_spec(dev)
        put(transport, tid, "job.json", json.dumps(device_job, ensure_ascii=False))
        put(transport, tid, "id", tid + "\n")
        put(transport, tid, "grant.json", json.dumps(job["grant"], ensure_ascii=False) + "\n")
        put(transport, tid, "prompt.1", job["prompt"])
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
        reply = for_device(dev, self.worker_root).call(["reserve", job["id"], str(dev.cap)], timeout=60).json()
        if reply.get("action") != "reserved":
            self.store.log(f"{job['id']} {dev.name}: no slot ({reply.get('held')}/{dev.cap} held)")
        return reply.get("action") == "reserved"

    def _place_locked(self, job: dict) -> str | None:
        table = None
        if job.get("fixed_host"):
            dev = self.device(job["fixed_host"])
        else:
            dev, table = bidding.bid(self.devices(), self.worker_root, repo=job.get("repo"), runner=job["runner"],
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

    def _inputs(self, inputs: list[str]) -> list[str]:
        private = [Path(p).expanduser().resolve() for p in self.worker.get("private_paths") or []]
        limit = int(settings.setting("input_max_mb", worker=self.worker))
        checked = []
        for raw in inputs:
            path = Path(raw).expanduser().resolve()
            if not path.exists():
                raise HubError(f"--input not found: {path}")
            if any(path == p or p in path.parents for p in private):
                raise HubError(f"--input is a private source ({path}); private originals are not handed to workers")
            size = sum(f.stat().st_size for f in path.rglob("*") if f.is_file()) if path.is_dir() else \
                path.stat().st_size
            if size > limit * (1 << 20):
                raise HubError(f"--input too big: {path} {size >> 20}MB > {limit}MB")
            checked.append(str(path))
        return checked

    def submit(self, opts: dict) -> int:
        briefs = [Path(b).expanduser().resolve() for b in opts["briefs"]]
        for brief in briefs:
            if not brief.is_file():
                raise HubError(f"brief not found: {brief}")
        opts.setdefault("bot", caller_agent())
        opts.setdefault("card", kanban_link.card_from_env())
        opts["topic"] = opts.get("topic") or opts.get("todo") or (opts["card"] or {}).get("task")
        if not opts["topic"]:
            raise HubError("--todo <to-do ID> is required (or --topic <short name> for work without a to-do)")
        if opts.get("at") or opts.get("every"):
            return self.schedule(opts)
        opts["inputs"] = self._inputs(opts.get("inputs") or [])
        group = self.store.new_id(opts["bot"], "OG") if len(briefs) > 1 else None
        jobs = [self._submit_one(brief, opts, group) for brief in briefs]
        if group:
            self.out(f"{group} batch of {len(jobs)} · one notice when all of them end.")
        elif opts.get("wait") is None:
            self.out(f"The end notice goes to {opts['bot']}'s chat. Do not poll.")
        if opts.get("wait") is not None and len(jobs) == 1:
            return self._wait(jobs[0]["id"], float(opts["wait"]))
        return 0

    def _submit_one(self, brief: Path, opts: dict, group: str | None) -> dict:
        job = self.make_job(brief, opts, group)
        if opts.get("wait") is not None and not group:
            job.update(self.store.update(job["id"], waiter=os.getpid(), waiter_start=_start_time(os.getpid())) or {})
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
        self.out(f"{job['id']} handed out · {job['bot']} · topic {job['topic']} · mode {job['mode']} · runner "
                 f"{job['runner']} · model {models.label(job['model'])} · "
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


    def schedule(self, opts: dict) -> int:
        when = time.time()
        if opts.get("at"):
            try:
                when = time.mktime(time.strptime(opts["at"], "%Y-%m-%d %H:%M"))
            except ValueError as exc:
                raise HubError('--at must look like "2026-11-01 09:00"') from exc
        every = parse_every(opts["every"]) if opts.get("every") else None
        sid = self.store.new_id(opts["bot"], "OS")
        saved = {k: v for k, v in opts.items() if k not in ("at", "every")}
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
            try:
                code = Hub(self.worker_root, out=lines.append).submit(dict(record["opts"]))
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
        head = (f"{job['id']} · {job['bot']} · {job['topic']} · mode {job.get('mode')} · runner {job.get('runner')} · "
                f"model {models.label(job.get('model'))}")
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


    def send(self, task_id: str, message: str) -> int:
        job = self.need(task_id)
        if not job.get("host"):
            raise HubError(f"{task_id} has not started yet (queued): cancel it and submit a new brief instead")
        if job.get("state") == "placing":
            raise HubError(f"{task_id} is still being handed to {job['host']}: send it again in a moment")
        transport, remote = self.transport(job), self.meta(job)
        if transport.call(["human", task_id], timeout=90).json().get("open"):
            reply = transport.call(["answer", task_id], message.encode("utf-8"), timeout=180).json()
            run = reply.get("run") or remote.get("run") or job.get("runs", 1)
            self.store.receipt(task_id, "answer", run=run, response={k: reply.get(k) for k in (
                "action", "steps", "still_pending", "reason")})
            if reply.get("action") == "answer" and reply.get("steps") and not reply.get("still_pending"):
                self.out(f"{task_id} answered the question of run {run}; the same run goes on.")
                return 0
            if not self._closed(job, int(run)):
                raise HubError(f"{task_id} run {run} did not close on its question; send again in a moment")
            message = "Answer to the earlier question: " + message
        elif remote.get("alive"):
            reply = transport.call(["send", task_id], message.encode("utf-8"), timeout=180).json()
            self.store.receipt(task_id, "steer", run=reply.get("run"), response=reply)
            if reply.get("action") in ("steered", "started"):
                self.out(f"{task_id} took the instruction into run {reply.get('run')}: it applies after the current "
                         "tool call, before the next decision.")
                return 0
            if reply.get("action") == "refused":
                raise HubError(f"{task_id}: runner {job.get('runner')} has no channel into a running turn; send it "
                               "again when this run ends")
            if reply.get("action") == "error":
                raise HubError(f"steer failed: {reply.get('reason')}")
        return self._next_run(job, message)

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

    def _next_run(self, job: dict, message: str) -> int:
        """Ask the device for the next run; it serializes this against a run still waiting, starting or running."""
        payload = {"prompt": follow_up_prompt(job.get("mode") or "ulw", message), "message": message}
        reply = self.transport(job).call(["follow_up", job["id"]], json.dumps(payload).encode("utf-8"),
                                         timeout=180).json()
        action, run = reply.get("action"), reply.get("run")
        if action == "queued":
            self.store.receipt(job["id"], "follow_up", input_id=f"{job['id']}.{run}", run=run, queued=True)
            self.out(f"{job['id']} run {run} has not started yet: the instruction was added to it.")
            return 0
        if action in ("steered", "started"):
            self.store.receipt(job["id"], "steer", run=run, response=reply)
            self.out(f"{job['id']} took the instruction into run {run}: it applies after the current tool call, before "
                     "the next decision.")
            return 0
        if action in ("busy", "refused"):
            raise HubError(f"{job['id']} run {run} is starting or running and cannot take an instruction now; send it "
                           "again when it is live or has ended")
        if action != "requested":
            raise HubError(f"follow-up failed: {reply.get('reason') or reply}")
        self.store.update(job["id"], runs=run, state="running", delivery="pending", verdict=None)
        self.store.receipt(job["id"], "follow_up", input_id=f"{job['id']}.{run}", run=run)
        self.out(f"{job['id']} started run {run} in the same session. The end notice will come.")
        return 0

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
        self.store.receipt(task_id, "verdict", verdict=verdict, note=note[:500], by=caller_agent(),
                           delivery=job.get("delivery"))
        self.out(f"{task_id} {verdict}")
        return 0


    def notify(self, task_id: str, kind: str, run: str) -> str:
        """Deliver the end of run *run* once, whoever reports it first (the device or the tick)."""
        with self.store.lock(task_id):
            job = self.store.load(task_id)
            if not job:
                self.store.log(f"{task_id} {kind}: no job record, skipped")
                return "no job"
            return self._notify_locked(job, kind, run)

    def _waiter_alive(self, job: dict) -> bool:
        """The ``submit --wait`` process is still the one that registered: ``(pid, start time)``, never a bare
        PID (a reused PID would swallow the end notice with nobody left to show it)."""
        pid = int(job.get("waiter") or 0)
        start = job.get("waiter_start")
        if not pid or start is None:
            return False
        return _start_time(pid) == start

    def _notice_body(self, job: dict, kind: str, run: str) -> tuple[str, Path]:
        out_dir, check = self.store.out_dir(job), {}
        if job.get("host") and kind != "unknown":
            try:
                out_dir, check = self.fetch(job)
            except (TransportError, HubError) as exc:
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
        marker = f"{kind}:{run}"
        if marker in job.get("notified", []):
            return "already"
        if str(run).isdigit() and int(run) >= 1 and int(run) != int(job.get("runs") or 1):
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
        text, _ = self._notice_body(job, kind, run)
        job.setdefault("notified", []).append(marker)
        job["notice"] = text
        self.store.save(job)
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

    def _card_end(self, job: dict, kind: str, run: str, text: str) -> None:
        try:
            kanban_link.record_end(job["card"], job, kind, run, text)
        except (ValueError, OSError) as exc:
            self.store.receipt(job["id"], "card_notice_failed", error=str(exc)[:300])
            return
        self.store.receipt(job["id"], "card_noticed", card=job["card"], state=kind, run=run)

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
        job.setdefault("human_since", now)
        marker = f"human:{run}"
        if now - job["human_since"] >= HUMAN_NOTICE_AFTER_S and marker not in job.get("notified", []):
            job.setdefault("notified", []).append(marker)
            how = self._deliver(job, notice.body(job, "human", run, self.store.out_dir(job), {}), f"human-{run}")
            self.store.receipt(job["id"], "notified", how=how[:200], state="human", run=run)
            if job.get("card"):
                self._card_end(job, "human", run, notice.body(job, "human", run, self.store.out_dir(job), {}))


def parse_every(value: str) -> int:
    match = re.fullmatch(r"(\d+)([mhd])", value or "")
    if not match:
        raise HubError(f"--every must look like 30m, 6h, 1d: {value}")
    return int(match.group(1)) * {"m": 60, "h": 3600, "d": 86400}[match.group(2)]


def notify(worker_root: Path, task_id: str, kind: str, run: str) -> str:
    return Hub(worker_root).notify(task_id, kind, run)


def tick(worker_root: Path) -> None:
    Hub(worker_root).tick()

