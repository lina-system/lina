"""The three kinds of end the hub records, and the one delivery id of each (lina#223).

Every end is one event: ``run.completed`` (a run ended; ``end_state`` is the runner's end), ``question`` (a run waits
on a person) or ``host.unavailable`` (a node cannot be reached; ``node_watch``). The event goes on the ``exec_end``
and ``notified`` receipts, the job's ``notified`` list, the card's event and comment, and the notice, whose text
starts with it (``run.completed (done)``).

Its delivery id is ``<task>:<run>:<event>`` (a question adds ``:<question key>``) or
``<node>:host.unavailable:<outage start>``. A run's end is held on its job (``deliveries_pending``), then staged (sent,
or on a Hermes card written as card work), then done: its id appended to ``hub/deliveries.jsonl`` (the ``deliveries``
lock) and marked on the job (``Hub._end_advance`` makes every move). An id in that file is done: a repeat (a device
retry, the tick, a hub restart) is recorded as ``delivery_duplicate`` and sent nowhere. An id held but not done (the
send failed, or the hub stopped before it) is moved on by the next report or tick. Job files from before keep
``<state>:<run>`` and ``human:<run>[:<key>]`` markers, which count as delivered too.
"""

from __future__ import annotations

import json
import time

RUN_COMPLETED, QUESTION, HOST_UNAVAILABLE = "run.completed", "question", "host.unavailable"
END_STATES = ("done", "done_empty", "review", "stalled", "failed", "cancelled")
# The hub's end states (``human``: a run waits on a person, ``unknown``: its node cannot be reached) by event.
EVENTS = {**dict.fromkeys(END_STATES, RUN_COMPLETED), "human": QUESTION, "unknown": HOST_UNAVAILABLE}


def fields(state: str) -> dict:
    """``event`` of the hub's end *state*, with ``end_state`` for ``run.completed``."""
    event = EVENTS.get(state, RUN_COMPLETED)
    return {"event": event, "end_state": state} if event == RUN_COMPLETED else {"event": event}


def headline(state: str) -> str:
    """What a notice or comment starts with: ``run.completed (done)``, ``question``, ``host.unavailable``."""
    named = fields(state)
    return f"{named['event']} ({named['end_state']})" if "end_state" in named else named["event"]


def delivery_id(task_id: str, run, state: str, key: str | None = None) -> str:
    return ":".join([task_id, str(run), fields(state)["event"], *([key] if key else [])])


def outage_id(node: str, since: float) -> str:
    return f"{node}:{HOST_UNAVAILABLE}:{int(since)}"


def marker(task_id: str, state: str, run, key: str | None = None) -> dict:
    """The job's ``notified`` entry for one end."""
    return {"delivery": delivery_id(task_id, run, state, key), "run": str(run), **fields(state)}


def _old_markers(state: str, run, key: str | None) -> set[str]:
    event = fields(state)["event"]
    if event == RUN_COMPLETED:
        return {f"{end}:{run}" for end in (*END_STATES, state)}
    if event == QUESTION:
        return {f"human:{run}", f"human:{run}:{key or '-'}"}
    return {f"unknown:{run}"}


def delivered(job: dict, state: str, run, key: str | None = None) -> bool:
    """The job's ``notified`` list holds this end, in either form."""
    did, old = delivery_id(job["id"], run, state, key), _old_markers(state, run, key)
    return any(entry.get("delivery") == did if isinstance(entry, dict) else entry in old
               for entry in job.get("notified") or [])


def pending(job: dict, state: str, run, key: str | None = None) -> str | None:
    """The key of this end's card work still to do (``card_pending``), in either form."""
    held = job.get("card_pending") or {}
    return next((name for name in (delivery_id(job["id"], run, state, key), *_old_markers(state, run, key))
                 if name in held), None)


def signed(text: str, delivery: str | None) -> str:
    """*text* with its delivery id as the last line."""
    return f"{text.rstrip()}\n\ndelivery {delivery}\n" if delivery else text


def done(store, delivery: str) -> bool:
    """*delivery* went out (it is in ``deliveries.jsonl``)."""
    with store.lock("deliveries"):
        return delivery in _delivered_ids(store)


def record(store, delivery: str, **fields_) -> None:
    """Record *delivery* as done, once its send returned."""
    with store.lock("deliveries"):
        if delivery in _delivered_ids(store):
            return
        store.root.mkdir(parents=True, exist_ok=True)
        with store.deliveries.open("a", encoding="utf-8") as fh:
            fh.write(json.dumps({"id": delivery, "at": time.strftime("%FT%T%z"), **fields_}, ensure_ascii=False,
                                default=str) + "\n")


def _delivered_ids(store) -> set[str]:
    if not store.deliveries.is_file():
        return set()
    ids = set()
    for line in store.deliveries.read_text(encoding="utf-8-sig").splitlines():
        try:
            ids.add(json.loads(line)["id"])
        except (ValueError, KeyError, TypeError):  # a line cut short by a hub that stopped while appending it
            continue
    return ids


def duplicate(store, task_id: str, state: str, run, delivery: str) -> None:
    store.receipt(task_id, "delivery_duplicate", delivery=delivery, state=state, run=str(run), **fields(state))
