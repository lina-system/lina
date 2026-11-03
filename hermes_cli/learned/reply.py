"""Persist owner decisions against frozen sent-card lines."""
from datetime import datetime, UTC
import json
import re
import time
from hermes_cli.learned.locale import word
from hermes_cli.learned.reply_parse import (Unparsed, cards, parse, resolve, split_header, pick, strongest, WORDS, records)
UNPARSED, NO_CARD, UNKNOWN = 3, 1, 2

def stamp():
    return datetime.fromtimestamp(time.time(), UTC).astimezone().isoformat(timespec="seconds")


def rows_for(card, items, parsed_by, mask):
    at, rows = stamp(), []
    for item, (n, verdict, text) in sorted(items.items(), key=lambda pair: (pair[1][0], pair[0])):
        row = dict(at=at, day=card["day"], n=n, item=item, verdict=verdict, parsed_by=parsed_by)
        if text:
            row["text"] = mask(text)[:600]
        rows.append(row)
    return rows


def summary(card, chosen):
    parts = []
    for verdict in WORDS:
        lines = [str(n) for n in sorted(chosen) if chosen[n][-2] == verdict]
        if lines:
            parts.append("·".join(lines) + " " + {"ok": "OK", "drop": word("s19"), "fix": word("s57")}[verdict])
    return word("s56").format(card["day"][5:], ", ".join(parts))


def append(state, rows, storage):
    with storage.State(state):
        path = state / "verdicts.jsonl"
        with path.open("a", encoding="utf-8") as out:
            out.write("".join(json.dumps(row, ensure_ascii=False) + "\n" for row in rows))
        path.chmod(0o600)


def reply(config, text, storage, mask):
    suffix, text = split_header(text)
    card = pick(config.state, suffix)
    if card is None:
        return NO_CARD, word("s45")
    try:
        chosen = parse(text, len(card["lines"]))
        items = resolve(card, chosen)
        if not items:
            raise Unparsed("nothing")
    except Unparsed:
        return UNPARSED, word("s46").format(card["day"][5:])
    append(config.state, rows_for(card, items, "reply", mask), storage)
    return 0, summary(card, chosen)


def verdict(config, item, value, text, storage, mask):
    if value == "fix" and not (text or "").strip():
        return UNKNOWN, word("s47")
    given = (value, (text or "").strip() or None)
    for card in cards(config.state):
        for line in card["lines"]:
            if item == line.get("id") or item in line["ids"]:
                if item == line.get("id"):
                    return record_line(config, card, line, given, storage, mask)
                append(config.state, rows_for(card, {item: (line["n"],) + given}, "bot", mask), storage)
                return 0, summary(card, {line["n"]: given}) + word("s48")
    return UNKNOWN, word("s49")


def verdict_line(config, day, n, value, text, storage, mask):
    """Record a bot-read verdict for line n as the owner saw it: cards/<day>.json only, never today's items."""
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", day):
        return UNKNOWN, word("s50")
    if value == "fix" and not (text or "").strip():
        return UNKNOWN, word("s47")
    card = storage.read_json(config.state / "cards" / (day + ".json"), None)
    line = next((line for line in card["lines"] if line["n"] == n), None) if card and card.get("day") == day else None
    if line is None:
        return UNPARSED, word("s51").format(day[5:], n)
    return record_line(config, card, line, (value, (text or "").strip() or None), storage, mask)


def record_line(config, card, line, given, storage, mask):
    try:
        items = shared(config.state, card, line, given)
    except Unparsed:
        return UNPARSED, word("s52")
    kept = len(line["ids"]) - len(items)
    if not items:
        return UNPARSED, word("s53")
    append(config.state, rows_for(card, items, "bot", mask), storage)
    return 0, summary(card, {line["n"]: given}) + word("s54").format(
        word("s55").format(kept) if kept else "")


def shared(state, card, line, given):
    """Expand a line id. An item another line also carries keeps that line's recorded fix or drop
    over ok; this line's own earlier records are replaced, never compared."""
    others = {item for other in card["lines"] if other["n"] != line["n"] for item in other["ids"]}
    latest = {}
    for row in records(state / "verdicts.jsonl"):
        if row.get("day") == card["day"] and row.get("verdict") in WORDS and row.get("n") != line["n"]:
            latest.setdefault(row.get("item"), {})[row.get("n")] = (row["verdict"], row.get("text"))
    items = {}
    for item in line["ids"]:
        recorded = list(latest.get(item, {}).values()) if item in others else []
        value = strongest([given] + recorded) if recorded else given
        if value == given:
            items[item] = (line["n"],) + given
    return items
