"""Append-only reaction journal (``heart`` rows in the memory store) and own-profile review input."""

from __future__ import annotations

from hermes_cli.heart import evidence, rows, storage
from hermes_cli.heart.locale import text
from hermes_cli.heart.models import Change, Entry, Pref
from hermes_cli.heart.parser import DEFAULT_CANDIDATE_LIMIT

PREF_FLAGS = ("pref_target", "pref_kind", "pref_dir", "pref_route")


def entries() -> list[Entry]:
    with rows.open_store(storage.home()) as store:
        return rows.entries(store)


def conclusion(entry: Entry) -> str:
    return text("conclusion", event=entry.event, feeling=entry.feeling, note=entry.note,
                ids=",".join(entry.evidence), strength=entry.strength)


def one_line(value: str, field: str, limit: int = 200) -> str:
    value = value.strip()
    if not value or len(value) > limit or any(char in value for char in ("\n", "\r", "|")):
        raise storage.HeartError(text("invalid_line", field=field, limit=limit))
    return value


def pref(args) -> Pref | None:
    given = [getattr(args, flag) for flag in PREF_FLAGS]
    if not any(value is not None for value in given):
        return None
    if any(value is None for value in given):
        raise storage.HeartError(text("pref"))
    if args.pref_dir == "ignored" and args.pref_route != "reaction":
        raise storage.HeartError("Only an owner reaction can be ignored")
    if args.pref_route == "consistency" and args.via != "review":
        raise storage.HeartError("Consistency evidence comes from the daily review")
    if args.pref_route in ("consistency", "exposure") and args.pref_dir != "like":
        raise storage.HeartError("Exposure and consistency carry positive evidence")
    return Pref(target=one_line(args.pref_target, "--pref-target", 80), kind=args.pref_kind,
                dir=args.pref_dir, route=args.pref_route)


def write(args) -> None:
    event = one_line(args.event, "--event")
    note = one_line(args.note, "--note")
    feeling = one_line(args.feeling, "--feeling", 40)
    if any(char.isspace() for char in feeling):
        raise storage.HeartError(text("feeling"))
    entry = Entry(ts=storage.stamp(), event=event, note=note, feeling=feeling, strength=args.strength,
                  via=args.via, pref=pref(args), evidence=())
    entry = entry.model_copy(update={"evidence": evidence.lookup(args.messages, reaction=entry.reaction)})
    home = storage.home()
    # The heart directory marks an agent that keeps a journal (daily review, weekly page).
    storage.private_dir(home / "heart")
    with rows.open_store(home) as store:
        uid = rows.row_uid(store.agent_id, entry.model_dump(mode="json", exclude_none=True))
        rows.put(store, [rows.row(uid, entry, conclusion(entry))])
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
    # An imported journal may exist before the profile has a local conversation.
    if (storage.home() / "state.db").is_file():
        evidence.print_candidates(day, evidence.day_candidates(day, DEFAULT_CANDIDATE_LIMIT))
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
