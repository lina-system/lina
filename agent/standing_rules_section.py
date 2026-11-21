"""The owner's rules in the frozen section and the difference on the next user row."""

from __future__ import annotations

import hashlib
import logging
from contextlib import closing, nullcontext
from typing import Any

from agent.message_content import flatten_message_text
from agent.safe_worker_policy import safe_worker_enabled
from hermes_cli.plugins import MAX_SYSTEM_PROMPT_SECTION_CHARS, RenderedPluginSystemPromptSection

logger = logging.getLogger(__name__)
SECTION_ID = "owner-standing-rules"
BOUNDARY = (
    "The owner's standing rules outrank tastes and memory. More specific rules win. "
    "Approval, honesty, secrets, the owner's decisions and safety always come first."
)
_OVERFLOW = "Rules exceed the frozen section budget:"


def read_rule_lines(agent: Any) -> tuple[str, ...]:
    from agent.system_prompt import _agent_home
    from hermes_constants import get_hermes_home
    from hermes_state_store import MessageStore, agent_id_for

    db = getattr(agent, "_session_db", None)
    sid = getattr(agent, "session_id", "") or ""
    gateway = getattr(agent, "_gateway_session_key", "") or ""
    places = []
    if gateway:
        places.append(f"channel:{gateway}")
    # A managed worker's RuntimeSessionStore offers neither the goal registry nor the owner's message
    # store (its reads are a closed set). Such a store has no goal place, and the rules come from the
    # agent's own handle, as for an agent without a session_db.
    get_meta = getattr(db, "get_meta", None)
    if get_meta is not None and sid:
        from hermes_cli.goals import GoalState, _meta_key
        goal = get_meta(_meta_key(sid))
        if goal and GoalState.from_json(goal).status in ("active", "paused"):
            places.append(f"goal:{sid}")
    owned = getattr(db, "message_store", None)
    store = (nullcontext(owned) if owned is not None else
             closing(MessageStore(agent_id_for((_agent_home(agent) or get_hermes_home()) / "state.db"))))
    with store as reader:
        rows = reader.standing_rules(sid, gateway, places)
    lines = []
    filters = 0
    for scope, place, date, statement in rows:
        if statement is None:
            filters += 1
        else:
            # Each record occupies one line. Literal frame delimiters must survive a stored-prompt round trip.
            text = " ".join(statement.splitlines()).replace("<!--", "&lt;!--")
            label = place.replace("<!--", "&lt;!--") if place else scope
            lines.append(f"- {label} ({date}): {text}")
    if filters:
        lines.append(f"Active filter rules: {filters}. To list them, read the applicable active filter rows in memory.standing_rules.")
    return tuple(lines)


def render_standing_rules_section(agent: Any) -> tuple:
    if safe_worker_enabled() or not (lines := read_rule_lines(agent)):
        return ()
    content = "\n".join((BOUNDARY, *lines))
    if len(content) > MAX_SYSTEM_PROMPT_SECTION_CHARS:
        content = (f"{BOUNDARY}\n{_OVERFLOW} {len(lines)} lines, {len(content)} characters. "
                   "The next user message carries the complete list.")
    return (RenderedPluginSystemPromptSection(id=SECTION_ID, content=content, position="after_memory", plugin="core"),)


def standing_rules_note(agent: Any, history: list) -> str:
    """Compare against the actual frozen frame. A fresh gateway agent recovers the last notice from history."""
    snapshot = getattr(agent, "_plugin_system_prompt_sections_snapshot", None)
    # A built or restored prompt can have an empty snapshot, which is a valid baseline.
    if snapshot is None or safe_worker_enabled():
        return ""
    # MoA and codex_app_server turns never stamp the api_content sidecar, so a sent notice could not be
    # read back from history and would repeat on every later turn (and replay would drop it from the
    # cached prefix). Those modes skip it, as stage_skills_index_note does.
    if getattr(agent, "provider", None) == "moa" or getattr(agent, "api_mode", None) == "codex_app_server":
        return ""
    try:
        current = read_rule_lines(agent)
    except Exception as exc:
        logger.warning("Could not read standing-rule changes (%s)", type(exc).__name__)
        return ""
    frozen = next((s.content for s in snapshot if s.id == SECTION_ID), "")
    baseline = tuple(line for line in frozen.splitlines()[1:] if not line.startswith(_OVERFLOW))
    # Tie the last announcement to its frame so an older notice cannot undo a successful compaction.
    frame = hashlib.sha256(frozen.encode()).hexdigest()[:16]
    prefix = f"[System: Standing rules update for frozen section {frame}.\n"
    previous = ""
    for row in reversed(history):
        if row.get("role") == "user":
            text = flatten_message_text(row.get("api_content") or row.get("content"))
            if prefix in text:
                previous = text.rsplit(prefix, 1)[1].partition("\nEnd standing rules update.]")[0]
                break
    if current == baseline and not previous:
        return ""
    from collections import Counter
    removed = tuple((Counter(baseline) - Counter(current)).elements())
    added = tuple((Counter(current) - Counter(baseline)).elements())
    parts = []
    if removed:
        parts.extend(("No longer applicable:", *removed))
    if added:
        parts.extend(("Applies now:", *added))
    difference = "\n".join(parts) or "The frozen rules are accurate again."
    if previous == difference:
        return ""
    return f"{prefix}{difference}\nEnd standing rules update.]"
