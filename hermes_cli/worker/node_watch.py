"""The tick's watch over the nodes that hold runs (lina#223): ``host.unavailable`` once per outage per node.

Each tick asks the device about every run that is running or ``unknown`` on it (``meta``). A tick on which none of
those calls was answered (each came back unreachable, or missing for a running run) is one failed contact; the record
``hub/nodes/<node>.json`` counts them in a row (``failures``, ``down_since``: the first one, ``notified_outage``: the
delivery id once sent). The third one sends ``host.unavailable``: one notice naming the node, the outage start and
its running runs, which become ``unknown`` (no notice of their own), to each agent they belong to, and one comment
per card. While the node stays down nothing more is sent. The first answered tick afterwards sends one line,
``<node> is back after <duration>; runs <ids> continue``, and resets the record. A run left ``unknown`` whose device
answers ``running`` or an end goes back under the tick's watch (``Hub._watch``), so its end is delivered once like any
other.
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
    record = hub.store.load_node(node) or {"node": node, "failures": 0, "down_since": None, "notified_outage": None}
    record["failures"] += 1
    record["down_since"] = record["down_since"] or time.time()
    if record["failures"] >= FAILED_CONTACTS and not record["notified_outage"]:
        _unavailable(hub, node, record, [job for job in jobs if job.get("state") == "running"])
    hub.store.save_node(record)


def _unavailable(hub, node: str, record: dict, running: list[dict]) -> None:
    delivery = events.outage_id(node, record["down_since"])
    named = [job for job in running if mark_unknown(hub.store, job["id"], delivery)]
    if not named:
        return
    record.update(notified_outage=delivery, runs=[job["id"] for job in named])
    if not events.claim(hub.store, delivery, node=node, runs=record["runs"]):
        for job in named:
            events.duplicate(hub.store, job["id"], "unknown", "x", delivery)
        return
    payload = {"device": node, "since": record["down_since"], "delivery": delivery, **events.fields("unknown")}
    how = _tell(hub, named, events.signed(_outage_text(node, record, named), delivery),
                f"{events.HOST_UNAVAILABLE}-{node}", payload)
    for job in named:
        hub.store.receipt(job["id"], "notified", how=how[:200], state="unknown", run="x", delivery=delivery,
                          **events.fields("unknown"))
    hub.store.log(f"{node} {events.HOST_UNAVAILABLE} {delivery}: {how}")


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
    record = hub.store.load_node(node)
    if not record or not record.get("failures"):
        return
    hub.store.save_node({"node": node, "failures": 0, "down_since": None, "notified_outage": None})
    if not record.get("notified_outage"):
        return
    from hermes_cli.worker.hub import _ago
    going = [job["id"] for job in jobs if replies[job["id"]].get("state") == "running"]
    line = f"{node} is back after {_ago(record['down_since'])}; " + \
        (f"runs {', '.join(going)} continue\n" if going else "no run continues\n")
    told = [job for job in map(hub.store.load, record.get("runs") or []) if job]
    how = _tell(hub, told, line, f"host.back-{node}", None)
    hub.store.log(f"{node} back after the outage {record['notified_outage']}: {how}")


def _back_under_watch(hub, task_id: str, node: str, reply: dict) -> None:
    """An ``unknown`` run its device answers for again is ``running`` on the hub: the tick delivers its end, if it has
    one, like any other. An end a device report recorded meanwhile stays."""
    with hub.store.edit(task_id) as job:
        back = job is not None and job.get("state") == "unknown"
        if back:
            job["state"] = "running"
    if back:
        hub.store.receipt(task_id, "host_back", host=node, state=reply.get("state"), run=reply.get("run"))


def _tell(hub, jobs: list[dict], text: str, tag: str, payload: dict | None) -> str:
    """*text* once to each agent whose runs these are (a run on a Hermes card is told on its card only) and once on
    each of their cards."""
    agents: dict[str, dict] = {}
    cards: dict[str, tuple[dict, list[str]]] = {}
    for job in jobs:
        if not kanban_link.on_hermes(job.get("card")):
            agents.setdefault(job["bot"], job)
        if job.get("card"):
            cards.setdefault(kanban_link.card_key(job["card"]), (job["card"], []))[1].append(job["id"])
    said = [hub._deliver(job, text, tag) for job in agents.values()]
    for card, runs in cards.values():
        try:
            kanban_link.record_host(card, kanban_link.config(hub.worker), text,
                                    {**payload, "run_ids": runs} if payload else None)
        except (ValueError, OSError, kanban_link.CardLinkError) as exc:
            for run in runs:
                hub.store.receipt(run, "card_notice_failed", step=tag, error=str(exc)[:300])
            said.append(f"card {card['task']}: failed ({str(exc)[:120]})")
            continue
        said.append(f"card {card['task']}")
    return "; ".join(said)
