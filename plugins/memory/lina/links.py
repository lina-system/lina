"""Linking a person's handles across channels, only as the owner says (lina#324, absorbing #313).

A handle is a person's key on one channel, written ``<source>:<key>``: ``gateway_user:<platform>:<user id>`` (a
gateway sender), ``messenger_author:<platform>:<author id>`` (an external message's ``author``),
``local_user:owner`` (the machine owner on local surfaces) or ``honcho_peer:<workspace>:<peer>``. A person is named
by a person id (``person:<hash>``) or by any handle they hold. One sentence of the owner confirms or undoes a link
(``memory_link``); the agent asks when it cannot tell which person is meant, and a shared display name is never a
link. Only confirmed links join: ``memory_search`` with a person finds that person's messages on every confirmed
channel and none of a same-name other's.

An owner's hand-kept alias list is imported as confirmed links (``import_aliases``). Its format, one person per line:

    # comment
    gateway_user:telegram:1001  messenger_author:kakao:k-77  messenger_author:line:u-5

The first entry (a handle or a person id) names the person; every handle after it is confirmed as theirs. Blank
lines and ``#`` comments are skipped; a line that names an unknown handle source fails the import before anything is
written.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from .identity import LINK_SOURCES, IdentityRegistry, SourceKey

LINK_TOOL = "memory_link"

LINK_SCHEMA = {
    "name": LINK_TOOL,
    "description": (
        "Link a person's handles across channels, only as the owner says. confirm: the owner said in plain words "
        "that this handle is this person; undo: the owner said it is not; list: a person's links; import: the "
        "owner's alias list file. Pass the owner's sentence as 'said'. A shared display name is never a reason to "
        "link; when you cannot tell which person the owner means, ask instead of calling this. Handles are "
        "<source>:<key>: gateway_user:<platform>:<user id>, messenger_author:<platform>:<author id>, "
        "local_user:owner, honcho_peer:<workspace>:<peer>; a person is a person id or any handle they hold."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "action": {"type": "string", "enum": ["confirm", "undo", "list", "import"]},
            "handle": {"type": "string", "description": "The handle to link or unlink (confirm, undo)."},
            "person": {"type": "string", "description": "The person: a person id or a handle they hold."},
            "said": {"type": "string", "description": "The owner's sentence that confirms or undoes the link."},
            "path": {"type": "string", "description": "The alias list file (import)."},
        },
        "required": ["action"],
    },
}


def parse_handle(text: str) -> SourceKey:
    source, _, key = (text or "").strip().partition(":")
    if source not in LINK_SOURCES or not key:
        raise ValueError(f"not a handle: {text!r}; a handle is <source>:<key> with source one of "
                         f"{', '.join(LINK_SOURCES)}")
    return source, key


def person_id(registry: IdentityRegistry, person: str) -> str:
    person = (person or "").strip()
    return person if person.startswith("person:") else registry.person_of(parse_handle(person))


def row_refs(handles: list[SourceKey]) -> list[str]:
    """The ``author`` / ``speaker_ref`` values of memory.messages rows the handles wrote (docs/store.md)."""
    return [key if source != "local_user" else f"local:{key}" for source, key in handles if source != "honcho_peer"]


def confirm(registry: IdentityRegistry, handle: str, person: str, said: str) -> dict[str, Any]:
    target = person_id(registry, person)
    return {"handle": handle, "person": target,
            "status": registry.link(parse_handle(handle), target, status="confirmed", evidence=said)}


def undo(registry: IdentityRegistry, handle: str, person: str, said: str) -> dict[str, Any]:
    target = person_id(registry, person)
    status = registry.link(parse_handle(handle), target, status="undone", evidence=said)
    return {"handle": handle, "person": target, "status": status or "no link"}


def parse_aliases(text: str) -> list[tuple[str, list[SourceKey]]]:
    """``[(person entry, [handle, ...])]`` of an alias list; raises ValueError naming the first bad line."""
    people = []
    for number, line in enumerate(text.splitlines(), 1):
        entries = line.split("#", 1)[0].split()
        if not entries:
            continue
        try:
            if not entries[0].startswith("person:"):
                parse_handle(entries[0])
            people.append((entries[0], [parse_handle(entry) for entry in entries[1:]]))
        except ValueError as exc:
            raise ValueError(f"alias list line {number}: {exc}") from None
    return people


def import_aliases(registry: IdentityRegistry, path: Path) -> dict[str, Any]:
    """Confirm every handle of the alias list at *path* as the person its line names."""
    people = parse_aliases(Path(path).read_text(encoding="utf-8-sig"))
    evidence = f"imported from the owner's alias list {Path(path).name}"
    linked = 0
    for entry, handles in people:
        target = person_id(registry, entry)
        for handle in handles:
            registry.link(handle, target, status="confirmed", evidence=evidence)
            linked += 1
    return {"people": len(people), "linked": linked}


def handle_tool_call(registry: IdentityRegistry, args: dict[str, Any]) -> str:
    from tools.registry import tool_error
    action = args.get("action")
    try:
        if action in ("confirm", "undo"):
            if not str(args.get("said") or "").strip():
                return tool_error("pass the owner's sentence as 'said'")
            run = confirm if action == "confirm" else undo
            result = run(registry, str(args.get("handle") or ""), str(args.get("person") or ""), str(args["said"]))
        elif action == "list":
            target = person_id(registry, str(args.get("person") or ""))
            result = {"person": target, "links": registry.links(target)}
        elif action == "import":
            result = import_aliases(registry, Path(str(args.get("path") or "")))
        else:
            return tool_error("action must be confirm, undo, list or import")
    except (ValueError, OSError) as exc:
        return tool_error(str(exc))
    return json.dumps(result, ensure_ascii=False)
