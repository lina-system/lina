"""Per-agent weekly counts and self-note diffs, never journal prose."""

from __future__ import annotations

import datetime as dt
import json
from collections import Counter
from pathlib import Path

from pydantic import TypeAdapter

from hermes_cli.heart import storage
from hermes_cli.heart.locale import text
from hermes_cli.heart.models import Change, Entry, Insight
from hermes_cli.profiles import get_profile_dir, list_profile_names
from hermes_cli.work.env import current


def read_insights(path: Path) -> dict[str, Insight]:
    raw = json.loads(path.read_text(encoding="utf-8-sig"))
    if isinstance(raw, dict):
        for key in ("profiles", "rows", "stats", "items"):
            if isinstance(raw.get(key), list):
                raw = raw[key]
                break
        else:
            raw = [dict(value, profile=name) for name, value in raw.items() if isinstance(value, dict)]
    return {row.profile: row for row in TypeAdapter(list[Insight]).validate_python(raw)}


def stats_line(row: Insight | None) -> str:
    if row is None:
        return text("no_stats")
    accuracy = text("no_accuracy") if row.accuracy is None else text("accuracy", value=round(row.accuracy * 100))
    return text("stats", total=row.open + row.closed, opened=row.open, closed=row.closed, answered=row.answered,
                correct=row.correct, incorrect=row.incorrect, unknown=row.unknown, accuracy=accuracy)


def in_week(timestamp: str, end: dt.date) -> bool:
    parsed = storage.parse_ts(timestamp)
    return parsed is not None and end - dt.timedelta(days=6) <= parsed.astimezone().date() <= end


def section(profile: str, end: dt.date) -> str:
    lines = [f"## {current().agents.get(profile, profile)}"]
    heart = get_profile_dir(profile) / "heart"
    if not heart.is_dir():
        return "\n".join(lines + [text("inactive")])
    entries = [entry for entry in storage.records(heart / "journal.jsonl", Entry) if in_week(entry.ts, end)]
    changes = [change for change in storage.records(heart / "changes.jsonl", Change) if in_week(change.ts, end)]
    applied = [change for change in changes if change.added or change.removed]
    refused = Counter(change.refused for change in changes if change.refused)
    review = sum(entry.via == "review" for entry in entries)
    head = text("count", count=len(entries))
    if entries:
        head += text("paths", turn=len(entries) - review, review=review)
    head += text("applies", count=len(applied))
    tallies = {"zero": sum(change.changed == 0 for change in changes), "refused_count": sum(refused.values()),
               "undo_count": sum(bool(change.undo) for change in changes)}
    for key, count in tallies.items():
        if count:
            head += text(key, count=count)
    lines.append(head + ".")
    for change in applied:
        lines.extend(text("removed", line=line) for line in change.removed)
        lines.extend(text("added", line=line) for line in change.added)
    if refused:
        lines.append(text("rules", rules=", ".join(f"{rule} {count}" for rule, count in sorted(refused.items()))))
    return "\n".join(lines)


def weekly(args) -> None:
    end = storage.day(args.week_ending)
    names = list_profile_names()
    insights = read_insights(Path(args.insight_stats)) if args.insight_stats else {}
    blocks = [text("weekly_title", day=end.isoformat()), "",
              text("weekly_intro", start=(end - dt.timedelta(days=6)).isoformat(), end=end.isoformat(), count=len(names))]
    for profile in names:
        blocks.extend(["", section(profile, end)])
        if args.insight_stats:
            blocks.append("- " + stats_line(insights.get(profile)))
    blocks.extend(["", text("headings"), ""])
    configured = storage.settings().weekly_out_dir
    output = Path(args.out_dir or configured or current().base / "heart-weekly").expanduser()
    if not output.is_absolute():
        output = current().base / output
    path = output / (end.strftime("%y%m%d") + ".md")
    storage.write_private(path, "\n".join(blocks))
    print(text("weekly", path=str(path)))
