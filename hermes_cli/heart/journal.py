"""Append-only reaction journal and own-profile review input."""

from __future__ import annotations

from hermes_cli.heart import evidence, storage
from hermes_cli.heart.locale import text
from hermes_cli.heart.models import Change, Entry


def entries() -> list[Entry]:
    return storage.records(storage.home() / "heart/journal.jsonl", Entry)


def conclusion(entry: Entry) -> str:
    return text("conclusion", event=entry.event, feeling=entry.feeling, note=entry.note,
                ids=",".join(str(item.message_id) for item in entry.evidence), strength=entry.strength)


def one_line(value: str, field: str, limit: int = 200) -> str:
    value = value.strip()
    if not value or len(value) > limit or any(char in value for char in ("\n", "\r", "|")):
        raise storage.HeartError(text("invalid_line", field=field, limit=limit))
    return value


def write(args) -> None:
    event = one_line(args.event, "--event")
    note = one_line(args.note, "--note")
    feeling = one_line(args.feeling, "--feeling", 40)
    if any(char.isspace() for char in feeling):
        raise storage.HeartError(text("feeling"))
    entry = Entry(ts=storage.stamp(), event=event, note=note, feeling=feeling, strength=args.strength,
                  via=args.via, evidence=evidence.lookup(args.messages))
    storage.append(storage.home() / "heart/journal.jsonl", entry)
    print(conclusion(entry))


def listing(args) -> None:
    selected = entries()
    if args.since:
        cutoff = storage.since(args.since)
        selected = [entry for entry in selected if (parsed := storage.parse_ts(entry.ts)) and parsed >= cutoff]
    if args.json:
        storage.json_print(selected)
    else:
        for entry in selected:
            print(conclusion(entry))


def review_input(args) -> None:
    from hermes_cli.heart.selfnote import removal_candidates

    day = storage.day(args.date)
    selected = [entry for entry in entries() if (parsed := storage.parse_ts(entry.ts)) and parsed.date() == day]
    print(text("journal", day=day.isoformat(), count=len(selected)))
    for entry in selected:
        print(f"{entry.ts}  {conclusion(entry)}")
    if not selected:
        print(text("no_journal"))
    soul = storage.load_text(storage.home() / "SOUL.self.md")
    print(text("soul", count=len(soul)))
    print(soul.rstrip("\n") if soul else text("no_soul"))
    changes = storage.records(storage.home() / "heart/changes.jsonl", Change)[-5:]
    print(text("changes", count=len(changes)))
    for change in changes:
        print(change.model_dump_json(exclude_defaults=True))
    if len(soul) > 1300 and (candidates := removal_candidates(soul)):
        print(text("removal") + " ; ".join(candidates))
