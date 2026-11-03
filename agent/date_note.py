"""Today's date reaches the model as a turn note, never in the cached system prompt (lina#21).

The prompt is byte-stable for the life of a conversation and names only its start date. A line that changes
with the day does not belong in it: a conversation that crosses midnight would otherwise either keep the
model on a stale day or rebuild the prompt and re-prefill everything behind it. ``system_prompt.date_line``
renders today's date once it differs from the start date; this module stages it on the per-turn
user-message note channel (the one the skills-index delta and the surface-switch note use, behind the
cached prefix) whenever it differs from the line the model was last given, so each new day is told once.

What was last given is read back from the transcript (a fresh agent per turn, the gateway shape, has no
memory of its own) or kept on a long-lived agent, as ``skills_index_delta`` does.
"""
from __future__ import annotations

from typing import Any

from agent.message_content import flatten_message_text

_NOTE_PREFIX = "[System: "
_NOTE_SUFFIX = "]"
_MARK = "Today's date: "


def _last_told(conversation_history: Any) -> str:
    for msg in reversed(conversation_history or []):
        if not isinstance(msg, dict) or msg.get("role") != "user":
            continue
        sidecar = msg.get("api_content")
        text = (sidecar if isinstance(sidecar, str) else "") + "\n" + flatten_message_text(msg.get("content"))
        for line in reversed(text.splitlines()):
            if line.startswith(_NOTE_PREFIX) and _MARK in line and line.endswith(_NOTE_SUFFIX):
                return line[len(_NOTE_PREFIX):-len(_NOTE_SUFFIX)]
    return ""


def stage_date_note(agent: Any, conversation_history: Any) -> bool:
    """Stage the date line as a one-shot note when it differs from the one last given. Returns whether it
    staged."""
    from agent.system_prompt import date_line

    line = date_line(agent)
    told = getattr(agent, "_date_note_told", None)
    if not isinstance(told, str):
        told = _last_told(conversation_history)
    agent._date_note_told = line
    if not line or line == told:
        return False
    agent._date_note = f"{_NOTE_PREFIX}{line}{_NOTE_SUFFIX}"
    return True
