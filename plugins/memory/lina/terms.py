"""``owner_terms``: what the owner called a concept in his own messages (lina#379, ``hermes_cli/korean/lookup.py``)."""

from __future__ import annotations

import json
from typing import Any

TERMS_TOOL = "owner_terms"

TERMS_SCHEMA = {
    "name": TERMS_TOOL,
    "description": (
        "Before naming a concept in Korean, look up what the owner called it in his own messages (never an "
        "agent's). Pass the concept as a plain Korean phrase or an English term. Returns his terms, most used "
        "first, each with how many of his messages used it and their uids (memory_open msg:<uid> opens one); "
        "'use' is the term to write. When 'terms' is empty, describe the concept in plain words and do not coin a "
        "name for it."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "concept": {"type": "string",
                        "description": "The concept: a Korean phrase (취향 목록) or an English term (taste slot)."},
        },
        "required": ["concept"],
    },
}


def handle_tool_call(store: Any, args: dict[str, Any]) -> str:
    from hermes_cli.korean.lookup import KoreanUnavailable, lookup
    from hermes_state_store import StoreUnavailableError
    from tools.registry import tool_error
    try:
        result = lookup(store, str(args.get("concept") or ""))
    except (ValueError, KoreanUnavailable, StoreUnavailableError) as exc:
        return tool_error(str(exc))
    return json.dumps(result, ensure_ascii=False)
