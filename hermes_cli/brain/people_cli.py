"""``lina brain people``: link a person page to a person, print a person's card, import Honcho's conclusions once."""

from __future__ import annotations

import sys


def build(subparsers) -> None:
    people = subparsers.add_parser("people", help="Person pages: the person card and Honcho's conclusions")
    commands = people.add_subparsers(dest="people_command", required=True)
    link = commands.add_parser(
        "link", help="Name a person in a person page's frontmatter (person_id), so the page is their one page",
        description="Write person_id into the frontmatter of person page PAGE (its slug) and index it again. "
                    "PERSON is a person id (person:...) or one of the person's handles (gateway_user:telegram:1001, "
                    "local_user:owner, messenger_author:kakao:k-1).")
    link.add_argument("page", metavar="PAGE")
    link.add_argument("person", metavar="PERSON")
    link.set_defaults(brain_fn=run_link)
    card = commands.add_parser("card", help="Print the person card a new session injects for PERSON")
    card.add_argument("person", metavar="PERSON")
    card.set_defaults(brain_fn=run_card)
    honcho = commands.add_parser(
        "import-honcho", help="Copy Honcho's conclusions about each linked person into their page, once",
        description="For every person page with a person_id, read the Honcho conclusions about that person's Honcho "
                    "peers in this home's workspace and append the ones not on the page yet as agent entries "
                    "(source honcho:<id>). Running it again adds nothing; nothing in Honcho is changed.")
    honcho.add_argument("--json", action="store_true", help="Print the report as JSON")
    honcho.set_defaults(brain_fn=run_import_honcho)


def _person(value: str) -> str:
    from plugins.memory.lina.identity import LINK_SOURCES, IdentityRegistry
    if value.startswith("person:"):
        return value
    source, _, key = value.partition(":")
    if source not in LINK_SOURCES or not key:
        raise SystemExit(f"brain: {value} is neither a person id nor a handle ({', '.join(LINK_SOURCES)})")
    registry = IdentityRegistry()
    try:
        return registry.person_of((source, key))
    finally:
        registry.close()


def run_link(args) -> int:
    from hermes_cli.brain import people
    from hermes_cli.brain.store import owner_connection
    person = _person(args.person)
    with owner_connection() as conn:
        file = people.link(conn, args.page, person)
    print(f"Linked {args.page} to {person} ({file.name}).")
    return 0


def run_card(args) -> int:
    from hermes_cli.brain import people
    from hermes_cli.brain.store import owner_connection
    person = _person(args.person)
    with owner_connection() as conn:
        text = people.card_for(conn, person)
    if text is None:
        print(f"brain: {person} has no person page", file=sys.stderr)
        return 1
    print(text)
    return 0


def run_import_honcho(args) -> int:
    from hermes_cli.brain import people
    from hermes_cli.brain.store import dumps, owner_connection
    from plugins.memory import import_provider_module
    from plugins.memory.lina.identity import IdentityRegistry
    facts = import_provider_module("honcho", "person_facts")
    try:
        workspace, list_conclusions = facts.home_lister()
    except facts.HonchoNotConfigured as error:
        print(f"brain: {error}", file=sys.stderr)
        return 1
    registry = IdentityRegistry()
    try:
        with owner_connection() as conn:
            report = people.import_honcho(conn, registry, workspace, list_conclusions)
    finally:
        registry.close()
    if args.json:
        print(dumps(report.as_json()))
    else:
        print(f"Honcho conclusions: {report.pages} linked person pages, {report.peers} Honcho peers, "
              f"{report.conclusions_seen} conclusions seen, {report.added} added "
              f"({report.pages_without_peer} pages without a Honcho peer in workspace {workspace}, "
              f"{report.peers_in_other_workspaces} peers in other workspaces).")
    return 0
