"""The tick's watch over the nodes that hold runs (lina#223): ``host.unavailable`` once per outage per node.

Each tick asks the device about every run that is running or ``unknown`` on it (``meta``). A tick on which none of
those calls was answered (each came back unreachable, or missing for a running run) is one failed contact; the record
``hub/nodes/<node>.json`` counts them in a row (``failures``, ``down_since``: the first one, ``notified_outage``: the
delivery id once sent). The third one sends ``host.unavailable``: one notice naming the node, the outage start and
its running runs, which become ``unknown`` (no notice of their own), to each agent they belong to, and one comment
per card. While the node stays down nothing more is sent. The first answered tick afterwards sends one line,
``<node> is back after <duration>; runs <ids> continue``, and resets the record. A run left ``unknown`` whose device
answers ``running`` or an end goes back under the tick's watch (``Hub._watch``), so its end is delivered once like any
other. A tick that finds no watched run on a node, or a placement published on it, resets its record too.

Each of those notices is queued on the node record first, one item per place it goes (``pending``: each agent's chat,
each card), and an item leaves the queue once it went out; the next ticks retry what failed and send nothing twice. The
outage's delivery id is done only once every place was told.

Every change to a node record is one read-modify-write under its lock (``Store.edit_node``), never held across a send:
a reset that lands while a notice is going out (a placement published on the node) stays.
"""

from __future__ import annotations

import time

from hermes_cli.worker import events, kanban_link

FAILED_CONTACTS = 3
WATCHED = ("running", "unknown")
WATCH_FOR_S = 7 * 86400


def watch(hub) -> None:
    """One contact with each node that holds a watched run; ``hub.contacts`` keeps each run's ``meta`` reply for
    ``Hub._watch``."""
    hub.contacts = {}
    for path in hub.store.nodes.glob("*.json"):
        if (record := hub.store.load_node(path.stem)) and record.get("pending"):
            hub.store.log(f"{path.stem} notices retried: {_flush(hub, path.stem)}")
    nodes: dict[str, list[dict]] = {}
    for job in hub.store.all():
        if job.get("state") in WATCHED and job.get("host") and time.time() - job["created"] < WATCH_FOR_S:
            nodes.setdefault(job["host"], []).append(job)
    for node, jobs in nodes.items():
        replies = {job["id"]: hub.meta(job) for job in jobs}
        hub.contacts.update(replies)
        if all(_failed(job, replies[job["id"]]) for job in jobs):
            _failed_contact(hub, node, jobs)
        else:
            _answered(hub, node, jobs, replies)
    for path in hub.store.nodes.glob("*.json"):
        if path.stem not in nodes:
            reset(hub.store, path.stem)


def reset(store, node: str) -> None:
    """*node* holds no outage any more (no watched run is left on it, or a placement on it was published): a later
    loss of contact counts from zero and is told again."""
    with store.edit_node(node) as record:
        _reset(record)


def _reset(record: dict) -> None:
    """No failed contact counted, no outage told; the notices still queued stay (``_flush`` drops an outage's that
    is not the record's any more)."""
    record.update(failures=0, down_since=None, notified_outage=None)
    record.pop("runs", None)


def outage_of(store, job: dict) -> str | None:
    """The delivery id of the newest outage that made *job* ``unknown``: the one its node record sent and names it
    while the run is still ``unknown``, else the one the job carries from ``mark_unknown`` (kept after its node is
    back). None when no outage of its node marked it."""
    node = job.get("host")
    if not node:
        return None
    record = store.load_node(node) or {}
    if job.get("state") == "unknown" and record.get("notified_outage") and job["id"] in (record.get("runs") or []):
        return record["notified_outage"]
    prefix = events.outage_id(node, 0).rsplit(":", 1)[0] + ":"
    return next((entry["delivery"] for entry in reversed(job.get("notified") or [])
                 if isinstance(entry, dict) and str(entry.get("delivery") or "").startswith(prefix)), None)


def outage_open(store, node: str, delivery: str) -> bool:
    """*node*'s outage *delivery* is still open: no tick has had an answer from the node since it was told."""
    return (store.load_node(node) or {}).get("notified_outage") == delivery


def _failed(job: dict, reply: dict) -> bool:
    return bool(reply.get("unreachable") or (reply.get("missing") and job.get("state") == "running"))


def mark_unknown(store, task_id: str, delivery: str | None = None) -> bool:
    """A running run whose node cannot be reached becomes ``unknown`` (the board, ``wait`` and the batch notice read
    it), with one ``exec_end`` receipt; False when it is not running any more."""
    with store.edit(task_id) as job:
        if job is None or job.get("state") != "running":
            return False
        job.update(state="unknown", last_state="unknown")
        if delivery:
            job.setdefault("notified", []).append({"delivery": delivery, "run": "x", **events.fields("unknown")})
    store.receipt(task_id, "exec_end", state="unknown", run="x", **events.fields("unknown"),
                  **({"delivery": delivery} if delivery else {}))
    return True


def _failed_contact(hub, node: str, jobs: list[dict]) -> None:
    with hub.store.edit_node(node) as record:
        record["failures"] += 1
        record["down_since"] = record["down_since"] or time.time()
        queued = record["failures"] >= FAILED_CONTACTS and not record["notified_outage"] and \
            _unavailable(hub, node, record, jobs)
    if queued:
        hub.store.log(f"{node} {events.HOST_UNAVAILABLE} {queued}: {_flush(hub, node)}")


def _unavailable(hub, node: str, record: dict, jobs: list[dict]) -> str | None:
    """The outage told once: its running runs become ``unknown`` first, then the notice is queued on the node record
    for each place it goes (sent by ``_flush`` once the record is saved); its id is done once every one of them went
    out. A hub that stopped before queueing it finds those runs ``unknown`` with this outage on them and tells it
    then. The delivery id when it was queued."""
    delivery = events.outage_id(node, record["down_since"])
    named = [job for job in jobs if _held(job, delivery) or mark_unknown(hub.store, job["id"], delivery)]
    if not named:
        return None
    record.update(notified_outage=delivery, runs=[job["id"] for job in named])
    if events.done(hub.store, delivery):
        for job in named:
            events.duplicate(hub.store, job["id"], "unknown", "x", delivery)
        return None
    payload = {"device": node, "since": record["down_since"], "delivery": delivery, **events.fields("unknown")}
    _queue(record, named, events.signed(_outage_text(node, record, named), delivery),
           f"{events.HOST_UNAVAILABLE}-{node}", payload, key=delivery, delivery=delivery)
    return delivery


def _held(job: dict, delivery: str) -> bool:
    return job.get("state") == "unknown" and any(isinstance(entry, dict) and entry.get("delivery") == delivery
                                                 for entry in job.get("notified") or [])


def _outage_text(node: str, record: dict, jobs: list[dict]) -> str:
    since = time.strftime("%Y-%m-%d %H:%M", time.localtime(record["down_since"]))
    runs = "".join(f"- {job['id']} · {job['bot']} · {job.get('todo') or job.get('topic')}"
                   + (f" · card {job['card']['task']}" if job.get("card") else "") + "\n" for job in jobs)
    return (f"{events.HOST_UNAVAILABLE} · [lina worker · automatic] {node} cannot be reached since {since} "
            f"({record['failures']} failed contacts in a row); its runs were not moved to another device:\n{runs}"
            "To do: nothing until it is back; one line follows then (`lina worker status <ID>` shows a run).\n")


def _answered(hub, node: str, jobs: list[dict], replies: dict[str, dict]) -> None:
    for job in jobs:
        reply = replies[job["id"]]
        if job.get("state") == "unknown" and not reply.get("unreachable") and not reply.get("missing") \
                and reply.get("state") in ("running", *events.END_STATES):
            _back_under_watch(hub, job["id"], node, reply)
    from hermes_cli.worker.hub import _ago
    with hub.store.edit_node(node) as record:
        outage, since, runs = record["notified_outage"], record["down_since"], record.get("runs") or []
        if not record["failures"]:
            return
        _reset(record)
        if outage:
            going = [job["id"] for job in jobs if replies[job["id"]].get("state") == "running"]
            line = f"{node} is back after {_ago(since)}; " + \
                (f"runs {', '.join(going)} continue\n" if going else "no run continues\n")
            _queue(record, [job for job in map(hub.store.load, runs) if job], line, f"host.back-{node}", None,
                   key=f"{outage}:back")
    if outage:
        hub.store.log(f"{node} back after the outage {outage}: {_flush(hub, node)}")


def _back_under_watch(hub, task_id: str, node: str, reply: dict) -> None:
    """An ``unknown`` run its device answers for again is ``running`` on the hub: the tick delivers its end, if it has
    one, like any other. An end a device report recorded meanwhile stays."""
    with hub.store.edit(task_id) as job:
        back = job is not None and job.get("state") == "unknown"
        if back:
            job["state"] = "running"
    if back:
        hub.store.receipt(task_id, "host_back", host=node, state=reply.get("state"), run=reply.get("run"))


def _queue(record: dict, jobs: list[dict], text: str, tag: str, payload: dict | None, *, key: str,
           delivery: str | None = None) -> None:
    """*text* queued on the node *record* (``pending``, under *key* and each place it goes): once to each agent whose
    runs these are (a run on a Hermes card is told on its card only) and once on each of their cards."""
    pending, runs = record.setdefault("pending", {}), [job["id"] for job in jobs]
    for job in jobs:
        common = {"text": text, "tag": tag, "delivery": delivery, "runs": runs}
        if not kanban_link.on_hermes(job.get("card")):
            pending.setdefault(f"{key} agent:{job['bot']}", {**common, "job": job["id"]})
        if job.get("card"):
            pending.setdefault(f"{key} card:{kanban_link.card_key(job['card'])}",
                               {**common, "card": job["card"], "card_runs": [], "payload": payload}
                               )["card_runs"].append(job["id"])


def _flush(hub, node: str) -> str:
    """Send each notice queued on *node*'s record, with no lock held while it goes. One for an outage the record no
    longer holds (it was reset: a placement published on the node, no run left on it) is dropped unsent. After each
    send the record is read again and only that item changes: one that went out leaves the queue, a failed one stays
    for the next tick, and one whose outage was reset while it went out is dropped. An outage whose every place was
    told is done (``deliveries.jsonl``, a ``notified`` receipt per run)."""
    with hub.store.edit_node(node) as record:
        queued = dict(record.get("pending") or {})
    said: dict[str | None, list[str]] = {}
    for name, item in queued.items():
        with hub.store.edit_node(node) as record:
            pending = record.get("pending") or {}
            if name in pending and item["delivery"] is not None and item["delivery"] != record["notified_outage"]:
                del pending[name]
        if name not in pending:
            continue
        how, sent = _send(hub, item)
        said.setdefault(item["delivery"], []).append(how)
        with hub.store.edit_node(node) as record:
            pending = record.get("pending") or {}
            stale = item["delivery"] is not None and item["delivery"] != record["notified_outage"]
            if name not in pending or not (sent or stale):
                continue
            del pending[name]
            done = sent and not stale and not any(other["delivery"] == item["delivery"] for other in pending.values())
        if done and item["delivery"]:
            _outage_done(hub, node, item, said[item["delivery"]])
    return "; ".join(how for hows in said.values() for how in hows)


def _outage_done(hub, node: str, item: dict, said: list[str]) -> None:
    events.record(hub.store, item["delivery"], node=node, runs=item["runs"])
    for run in item["runs"]:
        hub.store.receipt(run, "notified", how="; ".join(said)[:200], state="unknown", run="x",
                          delivery=item["delivery"], **events.fields("unknown"))


def _send(hub, item: dict) -> tuple[str, bool]:
    """One queued notice to its agent's chat or its card: how it went, and whether it went out."""
    if "card" not in item:
        job = hub.store.load(item["job"])
        if not job:
            return f"{item['job']}: no job record", True
        sent = hub._deliver(job, item["text"], item["tag"])
        return sent.how + ("" if sent.ok else ", retried on the next tick"), sent.ok
    card = item["card"]
    try:
        kanban_link.record_host(card, kanban_link.config(hub.worker), item["text"],
                                {**item["payload"], "run_ids": item["card_runs"]} if item["payload"] else None)
    except (ValueError, OSError, kanban_link.CardLinkError) as exc:
        for run in item["card_runs"]:
            hub.store.receipt(run, "card_notice_failed", step=item["tag"], error=str(exc)[:300])
        return f"card {card['task']}: failed ({str(exc)[:120]}), retried on the next tick", False
    return f"card {card['task']}", True
