"""Waits on another item (``work wait --on-item``): released when that item closes, told to the waiting agent."""

from __future__ import annotations

from hermes_cli.work import ledger
from hermes_cli.work.evaluate import item_name
from hermes_cli.work.locale import t
from hermes_cli.work.notify import tell
from hermes_cli.work.people import owner


def release_waits(recs: dict, closed: set, at: int, actor: str) -> list:
    """caller holds Lock(). Clear every open wait on an item that just closed; the cleared wait is kept on the
    record so the owner's 10-minute undo can put it back. -> [(waiter, closed item)]."""
    out = []
    for w in list(recs.values()):
        target = (w.get("wait") or {}).get("item")
        if target in closed and w.get("state") == "open" and w.get("id") not in closed:
            row = ledger.append(dict(w), "wait_release", actor, wait={},
                                released={"item": target, "at": at, "wait": w["wait"]})
            recs[w["id"]] = row
            out.append((row, recs[target]))
    return out


def tell_released(pairs, closed=(), requester: bool = True) -> None:
    """Each agent whose to-do waited on the owner's to-do hears it can go on; an owner's to-do nobody waited on is
    reported to the agent that asked for it. The owner hears nothing."""
    told = set()
    for waiter, target in pairs:
        if waiter["by"] != owner() and target.get("by") == owner():
            tell(waiter["by"], t("[대시보드] 주인이 '{done}'을 마쳤다. {next} 이어서 하면 된다", done=item_name(target),
                                 next=item_name(waiter)))
            told.add(target["id"])
    for r in closed:
        if requester and r.get("by") == owner() and r["id"] not in told:
            tell(r.get("from"), t("[대시보드] 주인이 '{done}'을 마쳤다.", done=item_name(r)))
