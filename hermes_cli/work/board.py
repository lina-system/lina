"""The work record as the board reads it (lina#39): the owner's own to-dos, the decisions aimed at items and the due
groups, each row with its ledger id. Reads only; the rules for "today" and an item's waits are the record's own
(``notify.owner_today``, ``wait``), the words are the locale's."""

from __future__ import annotations

import datetime

from hermes_cli.work.env import current

DUE_GROUPS = ("overdue", "today", "week", "later")
DUE_KINDS = ("목표", "할 일")


def agent_of(item: dict, recs: dict) -> str:
    """The agent an item answers to: its own ``by``, else its nearest parent's; the owner's own to-do answers to the
    agent that asked for it (``from``)."""
    env = current()
    if item.get("by") == env.owner:
        return item.get("from") if item.get("from") in env.agents else ""
    seen: set[str] = set()
    while item and item.get("id") not in seen:
        seen.add(item.get("id"))
        if item.get("by") in env.agents:
            return item["by"]
        item = recs.get(item.get("parent") or "")
    return ""


def due_day(item: dict) -> datetime.date | None:
    try:
        return datetime.date.fromisoformat(str(item.get("due") or ""))
    except ValueError:
        return None


def waits_on(item: dict, recs: dict) -> bool:
    """The owner's to-do still waits on something: an open item it names, a hold, or a wait sentence."""
    wait = item.get("wait") or {}
    target = recs.get(wait.get("item") or "")
    if target is not None:
        return target.get("state") == "open"
    return bool(wait.get("hold") or wait.get("on"))


def _planned_at(item: dict) -> float:
    return float((item.get("wait") or {}).get("next") or float("inf"))


def mine(recs: dict, order: list[str], today: datetime.date) -> list[tuple[dict, bool]]:
    """The owner's open to-dos as ``(item, waiting)``: ready ones first, then the waiting ones; each part past due,
    due today, due later (nearest first), then without a due date (oldest first)."""
    owner = current().owner

    def rank(item: dict):
        due = due_day(item)
        made = int(item.get("created") or 0)
        if due is None:
            return (3, 0, made)
        return (0 if due < today else 1 if due == today else 2, due.toordinal(), made)
    own = [recs[i] for i in order if recs[i].get("kind") == "할 일" and recs[i].get("by") == owner
           and recs[i].get("state") == "open"]
    rows = [(item, waits_on(item, recs)) for item in own]
    return sorted(rows, key=lambda row: (row[1], rank(row[0])))


def _decision_text(item: dict) -> tuple[str, str]:
    """(text, kind) of what the owner is asked on an item: its decision, else the sprint's owner line (a request)."""
    if item.get("decide"):
        text = str(item["decide"]).strip()
        return text, "ask"
    owner_line = str((item.get("sprint") or {}).get("owner") or "").strip()
    return (owner_line, "act") if owner_line else ("", "")


def _answered(item: dict) -> bool:
    """The owner answered the decision and no agent replied yet: the row waits for the agent, not the owner."""
    return any(c.get("kind") == "answer" and c.get("status") == "open" and not c.get("replies")
               for c in item.get("comments") or [])


def decisions(recs: dict, order: list[str]) -> list[dict]:
    """One row per open item that asks the owner something, oldest first; answered rows wait at the bottom."""
    rows = []
    for i in order:
        item = recs[i]
        if item.get("state") != "open" or (item.get("wait") or {}).get("hold"):
            continue
        text, kind = _decision_text(item)
        if not text:
            continue
        rows.append({"item": item, "agent": agent_of(item, recs), "kind": kind, "text": text,
                     "at": int(item.get("created") or 0), "answered": _answered(item)})
    return sorted(rows, key=lambda row: (row["answered"], row["at"]))


def due_groups(recs: dict, order: list[str], today: datetime.date) -> dict[str, list[dict]]:
    """Open goals and to-dos with a due date as overdue / today / this week (to Sunday) / later, each by due date
    then planned date (a dated wait), then age."""
    sunday = today + datetime.timedelta(days=6 - today.weekday())
    groups: dict[str, list[dict]] = {name: [] for name in DUE_GROUPS}
    for i in order:
        item = recs[i]
        due = due_day(item)
        if due is None or item.get("state") != "open" or item.get("kind") not in DUE_KINDS:
            continue
        name = "overdue" if due < today else "today" if due == today else "week" if due <= sunday else "later"
        groups[name].append(item)
    for rows in groups.values():
        rows.sort(key=lambda item: (due_day(item), _planned_at(item), int(item.get("created") or 0)))
    return groups


def inbox(recs: dict, order: list[str], today: datetime.date) -> tuple[list[dict], list[tuple[dict, bool]]]:
    """(decision rows, the owner's to-dos) with each item listed once: a to-do a decision row stands for (the item
    itself or the item its wait names) is left out of the to-dos."""
    asks = decisions(recs, order)
    covered = {row["item"].get("id") for row in asks} | {(row["item"].get("wait") or {}).get("item") for row in asks}
    return asks, [(item, waiting) for item, waiting in mine(recs, order, today) if item.get("id") not in covered]
