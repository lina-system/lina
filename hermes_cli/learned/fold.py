"""Deterministic daily views over masked collector items, without source access.

items/D.folded.json is a replaceable derived view. Raw items remain authoritative.
Optional verdicts.jsonl records use item, verdict or action (ok/fix/drop), at (ISO
or epoch), status, and, from `learned reply`/`learned verdict`, the card day and
line n. Later records update that item's current status. A fix/drop on card D's
line n counts once on day D+1's header (records without a card day count on the
day after their `at`), so previewing never consumes it. Items on a sent card's
lines with no ok/fix/drop three days after sending show as silent, never as OK.
"""
from collections import defaultdict
from datetime import date, datetime, timedelta
import importlib.util
import json
from pathlib import Path
import re
import time

from hermes_cli.learned import storage, text as display
from hermes_cli.work.common import mask as mask_text
from hermes_cli.learned.locale import word
FLAGS = {"regressed": 5, "no_basis": 3, "time_mismatch": 3, "self_closed": 3, "deleted": 2}
STATUSES = {"new", "shown", "ok", "fix", "drop", "silent", "applied", "failed"}
SILENT_AFTER = 3 * 86400


def day_value(value):
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", value):
        raise ValueError("invalid day")
    return date.fromisoformat(value)


def records(path):
    try:
        lines = path.read_text(encoding="utf-8-sig").splitlines()
    except FileNotFoundError:
        return []
    return [json.loads(line) for line in lines if line.strip()]


def masked(value):
    if isinstance(value, str):
        return mask_text(value)
    if isinstance(value, list):
        return [masked(part) for part in value]
    if isinstance(value, dict):
        return {mask_text(key): masked(part) for key, part in value.items()}
    return value


def rank(row):
    base = {"soul": 5, "rule": 5, "user": 4, "memory": 2, "conclusion": 1, "ledger": 1,
            "skill": 3 if row.get("actor") == "background" else 2}[row["kind"]]
    return base + sum(FLAGS.get(flag, 0) for flag in set(row.get("flags", [])))


def place(row, config):
    """Where a rule file lives, read from its unmasked path: a profile or a shared skill."""
    if row["kind"] not in ("rule", "soul") or "private" in row.get("flags", []):
        return None
    path = Path(row["target"])
    content = row.get("before", "") if "deleted" in row.get("flags", []) else row.get("after", "")
    shared = config.home / "shared-skills"
    if path.name == "SKILL.md" and shared in path.parents:
        return {"scope": "shared", "name": display.skill_name(content)}
    owners = [(len(home.parts), bot) for bot, home in config.profiles.items() if home in path.parents]
    if not owners:
        return None
    bot = max(owners)[1]
    if bot == "default" and (config.home / "profiles") in path.parents:
        return None
    if path.name == "SKILL.md":
        return {"scope": "profile", "bot": bot, "what": "skill", "name": display.skill_name(content)}
    stem = path.name.removesuffix(".md")
    return {"scope": "profile", "bot": bot, "what": "soul" if stem.upper().startswith("SOUL") else "rule",
            "file": stem if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]{0,23}", stem) else ""}


def clean(row, config=None):
    found = place(row, config) if config else None
    target = row["target"]
    if row["kind"] == "skill":
        target = ":".join(target.split(":")[:2])
    target_key = storage.digest(target)[:16]
    if "private" in row.get("flags", []):
        # Allowlist, including nested data: even old/handwritten private records
        # cannot expose target, descriptions, undo entries, basis or extra fields.
        row = {key: row[key] for key in ("id", "day", "kind", "bot", "actor", "ops", "status", "source", "flags")
               if key in row}
        row.update(target="private", before="", after="", summary=word("s1"), basis=[],
                   diff="", undo={"how": "none"})
        target_key = "private"
    if found:
        row["place"] = found
    row = masked(row)
    # Content masking can make unrelated long paths identical. Group by an
    # opaque pre-mask key while exporting only the masked target text.
    row["target_key"] = target_key
    for key in ("before", "after"):
        row[key] = row.get(key, "")[:300]
    row["flags"] = sorted(set(row.get("flags", [])))
    row["rank"] = rank(row)
    return row


def event_day(value):
    if isinstance(value, (float, int)):
        return datetime.fromtimestamp(value).date()
    return datetime.fromisoformat(value).astimezone().date()


def verdicts(root, day):
    statuses, decisions = {}, {}
    for event in records(root / "verdicts.jsonl"):
        item = event.get("item", event.get("item_id"))
        if not item:
            continue
        action = event.get("action", event.get("verdict"))
        if action in ("ok", "fix", "drop"):
            card = event.get("day")
            decisions[item] = (action, day_value(card) if card else event_day(event["at"]),
                               (card, event.get("n")) if card else item)
        status = event.get("status", action)
        if status in STATUSES:
            statuses[item] = status
    lines = defaultdict(list)
    for item, (action, at, line) in decisions.items():
        if action in ("fix", "drop") and at == day - timedelta(days=1):
            lines[line].append(item)
    carry = {"applied": 0, "pending": 0}
    for items in lines.values():
        carry["applied" if all(statuses.get(item) == "applied" for item in items) else "pending"] += 1
    return statuses, decisions, carry


def silent(root, day, decisions, now):
    card = storage.read_json(root / "cards" / (day + ".json"), None)
    if not card or now - datetime.fromisoformat(card["sent_at"]).timestamp() < SILENT_AFTER:
        return set()
    return {item for line in card["lines"] for item in line["ids"] if item not in decisions}



def build(config, day):
    from hermes_cli.learned.fold_groups import folded_items
    chosen_day = day_value(day)
    statuses, decisions, carry = verdicts(config.state, chosen_day)
    for item in silent(config.state, day, decisions, time.time()):
        statuses[item] = "silent"
    items = []
    for row in records(config.state / "items" / (day + ".jsonl")):
        if row["day"] != day:
            raise ValueError("item day mismatch")
        item = clean(row, config)
        item["status"] = statuses.get(item["id"], item.get("status", "new"))
        items.append(item)
    # Item files are appended in collection order; ids are digests.
    seq = {row["id"]: n for n, row in enumerate(items)}
    items.sort(key=lambda row: row["id"])
    return dict(version=1, day=day, items=items, folded=folded_items(items, config.profiles, seq), carry=carry)


def write(config, view):
    with storage.State(config.state):
        storage.atomic(config.state / "items" / (view["day"] + ".folded.json"),
                       json.dumps(view, ensure_ascii=False, sort_keys=True) + "\n")
