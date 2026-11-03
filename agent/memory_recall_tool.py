"""``memory_recall``: the read-only tool found memory arrives through (lina#21).

Each turn the runtime makes this call itself, right after the user message, and the turn's recall is
its result (``agent.turn_context.recall_tool_rows``). The call is in the history, so the tool must be in
the request too: strict providers reject a tool_use / function_call whose name no tool defines. The
memory manager lists it with the provider's tools while an external provider is on, so it sits in the
tool list from the session's first request and the cached tool prefix does not change. The model may
also call it with a query of its own.
"""

from __future__ import annotations

RECALL_TOOL_NAME = "memory_recall"

RECALL_TOOL_SCHEMA = {
    "name": RECALL_TOOL_NAME,
    "description": (
        "Read-only search of long-term memory (notes, people, past conversations). The runtime already "
        "runs it once per turn for the user's message and shows the result right after that message; "
        "call it yourself only to look up something else."
    ),
    "parameters": {
        "type": "object",
        "properties": {"query": {"type": "string", "description": "What to look up."}},
        "required": [],
    },
}
