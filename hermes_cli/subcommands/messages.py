"""Read shared external messages through the same search and open operations as agent tools."""

from __future__ import annotations

import json


def build_messages_parser(subparsers) -> None:
    parser = subparsers.add_parser("messages", help="Search or open stored external messages")
    actions = parser.add_subparsers(dest="messages_action", required=True)
    search = actions.add_parser("search", help="Find stored external messages")
    search.add_argument("query")
    search.add_argument("--limit", type=int)
    search.add_argument("--speaker", action="append", help="Limit results to this stored speaker kind")
    filters = search.add_mutually_exclusive_group()
    filters.add_argument("--sender", action="append")
    filters.add_argument("--person")
    opened = actions.add_parser("open", help="Read an external message by its msg:messages: reference")
    opened.add_argument("ref")
    opened.add_argument("--offset", type=int, default=0)
    parser.set_defaults(func=messages_command)


def messages_command(args) -> None:
    from hermes_constants import get_hermes_home
    from hermes_state_store import MessageStore, agent_id_for
    from plugins.memory.lina import links
    from plugins.memory.lina.identity import IdentityRegistry
    from plugins.memory.lina.search import MemorySearch, Readers, SourceOff

    def person_refs(person):
        registry = IdentityRegistry()
        return links.row_refs(registry.handles(links.person_id(registry, person)))

    store = MessageStore(agent_id_for(get_hermes_home() / "state.db"))
    reader = MemorySearch(Readers(store=lambda: store, person_refs=person_refs))
    try:
        if args.messages_action == "search":
            result = reader.search(args.query, ["messages"], args.limit, speakers=args.speaker,
                                   senders=args.sender, person=args.person)
        else:
            if not args.ref.startswith("msg:messages:"):
                raise ValueError("expected an external message reference")
            result = reader.open(args.ref, args.offset, pages=False)
    except (ValueError, SourceOff):
        raise SystemExit("Message or filter is unavailable.") from None
    finally:
        store.close()
    print(json.dumps(result, ensure_ascii=False))
