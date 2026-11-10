"""The wait of a run's ``wake`` on a Hermes card that is not ready for it yet (``kanban_link.CardWaiting``).

A wait is not a failing link (lina#473): it is said once per reason as ``card_notice_waiting``, retried at growing
gaps (the next tick, then ``GAPS`` apart) and given up after ``EXPIRE_S`` with one ``card_wake_expired`` receipt and
one comment on the card; the end is then noticed with ``woke: false``. A tick inside a gap reads no card and writes
nothing. The wait lives in the end's ``card_progress`` state of that card (``wait``), so a hub restart goes on with
it.
"""

from __future__ import annotations

import json
import time

GAPS = (60, 120)  # after the first retry on the next tick (the hub ticks every 30 s): 60 s, then every 120 s
EXPIRE_S = 24 * 3600


def resting(job: dict, entry: dict, now: float) -> bool:
    """Whether every card this end still owes is inside the gap of its wake wait: nothing to try on this tick."""
    from hermes_cli.worker.kanban_link import linked_cards
    if entry.get("fetch_pending"):
        return False
    progress = entry.get("card_progress") or {}
    owed = [progress.get(json.dumps(card, sort_keys=True)) for card in entry.get("cards") or []
            if card in linked_cards(job)]
    owed = [state for state in owed if not (state and state.get("noticed"))]
    return bool(owed) and all(state and _in_gap(state, now) for state in owed)


def _in_gap(state: dict, now: float) -> bool:
    wait = state.get("wait")
    return bool(wait) and "wake" not in state["done"] and now < wait["next"] and now - wait["since"] < EXPIRE_S


def gate(hub, job: dict, entry: dict, card: dict, state: dict, *, ticked: bool) -> str | None:
    """Before the wake step of *card*: None to try it now; the pending line when a tick lands inside a gap (or the
    expiry comment failed); "" when the wait expired and the step is over, not woken. A report of the end is not a
    tick: it tries the wake whatever the gap."""
    wait = state.get("wait")
    if not wait:
        return None
    now = time.time()
    if now - wait["since"] < EXPIRE_S:
        return f"card {card['task']}: wake waits ({wait['reason']})" if ticked and now < wait["next"] else None
    from hermes_cli.worker import kanban_link
    try:
        kanban_link.comment(kanban_link.config(hub.worker)["command"], card,
                            f"[lina worker · automatic] run {job['id']} (run {entry['run']}): the wake was not sent; "
                            f"the card waited {EXPIRE_S // 3600} h ({wait['reason']}).")
    except (OSError, ValueError, kanban_link.CardLinkError) as exc:
        hub.store.receipt(job["id"], "card_notice_failed", card=card, step="wake", error=str(exc)[:300])
        hub._card_failed(job, exc)
        return f"card {card['task']}: the wake expiry comment failed, retried on the next report or tick"
    hub.store.receipt(job["id"], "card_wake_expired", card=card, run=entry["run"], reason=wait["reason"],
                      tries=wait["tries"], since=wait["since"])
    return ""


def waited(hub, job: dict, card: dict, state: dict, exc) -> str:
    """Record one more try of a wake the card is not ready for; its receipt only for a reason not said yet."""
    now = time.time()
    wait = state.setdefault("wait", {"since": now, "tries": 0, "said": []})
    wait["tries"] += 1
    wait["reason"] = exc.reason
    wait["next"] = now + (0 if wait["tries"] == 1 else GAPS[min(wait["tries"] - 2, len(GAPS) - 1)])
    if exc.reason not in wait["said"]:
        wait["said"].append(exc.reason)
        hub.store.receipt(job["id"], "card_notice_waiting", card=card, step="wake", reason=exc.reason,
                          error=str(exc)[:300])
    hub.store.save(job)
    return f"card {card['task']}: wake waits ({exc.reason}), retried at growing gaps"
