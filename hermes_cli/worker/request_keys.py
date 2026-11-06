"""Request keys (lina#219): every write to the hub (``submit``, ``send``) carries a key, so a call retried after its
reply was lost (an ssh drop, a hub restart) is the same request. The same key with the same arguments returns the
first result and starts, places or sends nothing (receipt ``request_replayed``); the same key with other arguments is
refused and writes nothing.

The hub keeps one record per key, ``hub/keys/<sha256(key)>.json`` (``{key, op, args_sha256, task, run, action,
at}``), written under the key's lock before any device is called and filled in with the result after it. A hub that
stopped in between left the task ID there: the replay returns that task (the tick finishes its placement) and never
bids it out again. A send records the device verb it chose (``device_verb``, ``device_args``, ``device_phase``,
``device_key``) before calling it, and a retry sends that verb again rather than routing anew. Caller keys match
``CALLER_KEY``; a device call carries ``<caller key>#<phase>`` instead, so the two never meet. A device whose hello
lists ``request_keys`` keeps the reply of ``follow_up``, ``answer`` and ``send`` under the key alone (``keys.json`` in
the task folder), so any write verb sent again with a key it has seen changes nothing there.
"""

from __future__ import annotations

import hashlib
import json
import re
import time
import uuid
from pathlib import Path

CALLER_KEY = r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$"

# What defines a submit; --wait, --dry-notify and --note only change what the caller sees.
SUBMIT_FIELDS = ("mode", "repo", "todo", "topic", "host", "runner", "model", "labels", "size", "card", "bot", "at",
                 "every")


def new_key() -> str:
    return "rk-" + uuid.uuid4().hex


def checked(key: str) -> str:
    """A caller key; one outside :data:`CALLER_KEY` is refused before anything is written."""
    from hermes_cli.worker.hub import HubError
    if not re.fullmatch(CALLER_KEY, key):
        raise HubError(f"request key must match {CALLER_KEY}: {key}")
    return key


def device_key(key: str, phase: str) -> str:
    """The key a device call of request *key* carries: ``req`` for its first call, ``next`` for the run an answer
    falls back to. Caller keys never hold ``#``, so a caller key never equals a device key."""
    return f"{key}#{phase}"


def args_sha256(fields: dict) -> str:
    text = json.dumps(fields, sort_keys=True, ensure_ascii=False, separators=(",", ":"), default=str)
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def submit_args(opts: dict, briefs: list[Path]) -> str:
    fields = {name: opts.get(name) for name in SUBMIT_FIELDS}
    fields.update(access=opts.get("access") or "workspace-write",
                  briefs=[hashlib.sha256(brief.read_bytes()).hexdigest() for brief in briefs],
                  inputs=[str(Path(p).expanduser().resolve()) for p in opts.get("inputs") or []])
    return args_sha256(fields)


def _lock(hub, key: str):
    return hub.store.lock(f"key-{hub.store.key_name(key)}")


def _first(hub, key: str, op: str, digest: str) -> dict | None:
    """The record of *key*; refused when it was used for another request."""
    from hermes_cli.worker.hub import HubError
    record = hub.store.load_key(key)
    if record is not None and (record["op"] != op or record["args_sha256"] != digest):
        raise HubError(f"request key {key} was already used for {record['op']} {record['task']} at {record['at']} "
                       "with other arguments")
    return record


def _replayed(hub, record: dict, task_id: str, line: str) -> None:
    hub.store.receipt(task_id, "request_replayed", key=record["key"], op=record["op"], first_at=record["at"])
    hub.out(line)


def _new_record(hub, key: str, op: str, digest: str, task: str, **extra) -> dict:
    record = {"key": key, "op": op, "args_sha256": digest, "task": task, **extra, "run": None, "action": None,
              "at": time.strftime("%FT%T%z")}
    hub.store.save_key(record)
    return record


def submit(hub, opts: dict, briefs: list[Path]) -> int:
    """``Hub.submit`` with ``opts["request_key"]``. The task IDs are taken and recorded before anything is made, so
    a replay of a request the hub stopped in the middle of finds them."""
    key, digest = checked(opts["request_key"]), submit_args(opts, briefs)
    scheduled = bool(opts.get("at") or opts.get("every"))
    with _lock(hub, key):
        record = _first(hub, key, "submit", digest)
        if record is None and scheduled:
            record = _new_record(hub, key, "submit", digest, hub.store.new_id(opts["bot"], "OS"))
        elif record is None:
            tasks = [hub.store.new_id(opts["bot"]) for _ in briefs]
            group = hub.store.new_id(opts["bot"], "OG") if len(briefs) > 1 else None
            record = _new_record(hub, key, "submit", digest, group or tasks[0], tasks=tasks, group=group)
        if scheduled:
            return _schedule(hub, opts, record)
        made = _hand_out(hub, opts, briefs, record)
        record.update(run=1, action="submitted")
        hub.store.save_key(record)
    jobs = [hub.need(task_id) for task_id in record["tasks"]]
    if made:
        return hub.handed_out(opts, jobs)
    if opts.get("wait") is not None and len(jobs) == 1:
        return hub._wait(jobs[0]["id"], float(opts["wait"]))
    return 0


def _hand_out(hub, opts: dict, briefs: list[Path], record: dict) -> bool:
    """Make the tasks of *record* the hub does not hold yet; the ones it holds are replayed. True when any was made."""
    missing = []
    for brief, task_id in zip(briefs, record["tasks"]):
        job = hub.store.load(task_id)
        if job is None:
            missing.append((brief, task_id))
            continue
        if job.get("state") == "new":  # saved, never placed: the tick places it
            hub.store.update(task_id, state="queued")
            hub.store.receipt(task_id, "queued", why="request replayed before its placement")
        _replayed(hub, record, task_id, f"{task_id} replay of request key {record['key']}")
    if missing:
        hub.hand_out(opts, [brief for brief, _ in missing], [task_id for _, task_id in missing], record.get("group"))
    return bool(missing)


def _schedule(hub, opts: dict, record: dict) -> int:
    sid = record["task"]
    if (hub.store.schedules / f"{sid}.json").is_file():
        _replayed(hub, record, sid, f"{sid} replay of request key {record['key']}")
    else:
        hub.schedule(opts, sid)
    record["action"] = "scheduled"
    hub.store.save_key(record)
    return 0


def send(hub, task_id: str, message: str, card: dict | None, access: str | None, key: str) -> int:
    """``Hub.send`` with a request key. A record whose device call has no result (the hub or the device link
    stopped during it) sends that same device verb again with the same key (``Hub.resend``): a device that keeps keys
    answers with its first reply."""
    checked(key)
    digest = args_sha256({"task": task_id, "message": message, "access": access, "card": card})
    with _lock(hub, key):
        record = _first(hub, key, "send", digest)
        if record is not None and record.get("action"):
            _replayed(hub, record, task_id,
                      f"{task_id} replay of request key {key}: {record['action']} run {record['run']}")
            return 0
        if record is None:
            hub.need(task_id)
            record = _new_record(hub, key, "send", digest, task_id)
        with hub.store.lock(f"send-{task_id}"):
            if record.get("device_verb"):
                return hub.resend(task_id, record)
            return hub._send_locked(task_id, message, card, access=access, request_key=key)


def calling(store, key: str, verb: str, args: dict, phase: str) -> None:
    """Record the device verb a send chose, its arguments and the phase of the key it carries, before the call."""
    record = store.load_key(key)
    record.update(device_verb=verb, device_args=args, device_phase=phase, device_key=device_key(key, phase))
    store.save_key(record)


def sent(store, task_id: str, key: str | None, action: str, run) -> None:
    """Fill in what the send with *key* did, on its record and in the job's ``request_keys``."""
    if not key:
        return
    record = store.load_key(key)
    record.update(action=action, run=run)
    store.save_key(record)
    with store.edit(task_id) as job:
        job.setdefault("request_keys", {})[key] = {"action": action, "run": run, "at": record["at"]}


def reconcile(hub, task_id: str, key: str, reply: dict, *, access: str | None = None,
              run_access: str | None = None) -> int:
    """A reply the device kept for *key* (``replayed``): what it says happened before, so it is filled in and never
    applied as new. The task moves only to a run newer than the one it holds (another request may have started a
    later run since), and a replay never raises the task's access. A run the task holds that has ended (its end
    adopted the run) is left as it is."""
    from hermes_cli.worker.hub import ACCESS, ENDED
    action, run = reply["action"], reply.get("run")
    with hub.store.edit(task_id) as job:
        held = int(job.get("runs") or 1)
        if action == "requested" and int(run) > held:
            job.update(runs=run, state="running", delivery="pending", verdict=None)
        going = int(run or held) > held or (int(run or held) == held and job.get("state") not in ENDED)
        recorded = job.get("access", "workspace-write")
        if going and access in ACCESS and ACCESS.index(access) < ACCESS.index(recorded):
            job.update(access=access, grant={**job["grant"], "access": access})
        if going and run_access and action in ("queued", "requested"):
            job.setdefault("run_access", {}).setdefault(str(run), run_access)
    sent(hub.store, task_id, key, action, run)
    _replayed(hub, hub.store.load_key(key), task_id, f"{task_id} replay of request key {key}: {action} run {run}")
    return 0


def note_device(store, job: dict, transport) -> None:
    """A device whose hello does not list ``request_keys`` ignores the key: the hub's record is the only guard there,
    said once per task."""
    if ((transport.descriptor or {}).get("capabilities") or {}).get("request_keys"):
        return
    with store.edit(job["id"]) as stored:
        first = not stored.get("device_without_keys")
        stored["device_without_keys"] = True
    if first:
        store.receipt(job["id"], "device_without_keys", host=job.get("host"))
