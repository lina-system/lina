"""Reserve owner expression before the model call and settle its delivered reply."""

from __future__ import annotations

import logging
from contextlib import contextmanager
from dataclasses import dataclass
from hashlib import sha256
from pathlib import Path

from agent.message_metadata import append_message
from agent.skill_commands import extract_user_instruction_from_skill_message
from agent.taste_voice_policy import (
    render_turn_instructions, render_weekly, report_tools, report_turn,
    resolve_owner_preferences, verification_steps, voice_inputs,
)

logger = logging.getLogger(__name__)


@dataclass
class _VoiceTurn:
    home: Path
    turn_id: str
    source_uid: str
    task_kind: str | None
    disliked: bool
    claimed: bool


def _current_rows(messages, uid):
    for index in range(len(messages) - 1, -1, -1):
        row = messages[index]
        if row.get("role") == "user" and row.get("message_uid") == uid:
            return messages[index:]
    return []


def _picked_lines(current):
    from agent.turn_context_taste import _CLOSE, _OPEN, _is_generated_recall_call

    if len(current) != 3:
        return ()
    _, call, result = current
    if result.get("role") != "tool" or not _is_generated_recall_call(call, result.get("tool_call_id")):
        return ()
    content = result.get("content") or ""
    marker = _OPEN + "This agent's own tastes for this turn:\n"
    if content.endswith(_CLOSE) and marker in content:
        return tuple(content[:-len(_CLOSE)].rsplit(marker, 1)[1].splitlines())
    return ()


def _prepare(agent, home, messages, current):
    from agent.taste_voice_store import reserve, task_targets
    from agent.turn_context import recall_tool_rows
    from agent.turn_context_taste import _CLOSE, _OPEN
    from hermes_cli.heart.locale import protected_rule

    source = current[0]
    original = agent._persist_user_message_override
    query = extract_user_instruction_from_skill_message(
        original if original is not None else source.get("content")
    ) or ""
    picks = tuple(line for line in _picked_lines(current) if not protected_rule(line.partition(": ")[2]))
    targets = task_targets(home, picks)
    preferences = resolve_owner_preferences(query, picks)
    difference, dislike = voice_inputs(query, picks, owner_preferences=preferences, task_targets=targets)
    task_label = dislike or next((label for line in picks if (label := line.partition(": ")[2]) in targets), None)
    task_kind = "task_type:" + sha256(task_label.encode()).hexdigest() if task_label else None
    turn_id = agent._current_turn_id
    changes, say_difference, say_dislike = reserve(
        home, turn_id, agent._current_task_id, allow_difference=bool(difference), dislike=bool(dislike),
    )
    state = _VoiceTurn(home, turn_id, source["message_uid"], task_kind,
                       any(line.startswith("dislike: ") for line in picks), bool(changes or say_difference or say_dislike))
    agent._taste_voice_turn = state
    instructions = render_turn_instructions(difference if say_difference else None, dislike if say_dislike else None)
    if instructions:
        # The first recall result was already persisted at turn start. Append a
        # distinct call/result so the model request and a resumed transcript agree.
        for row in recall_tool_rows(agent, "", len(messages), taste_suffix=_OPEN + instructions + _CLOSE):
            append_message(messages, row)
        agent._flush_messages_to_session_db(messages)


def prepare(agent, home):
    uid = getattr(agent, "_taste_voice_source_uid", None)
    messages = getattr(agent, "_session_messages", None)
    if not uid or not isinstance(messages, list):
        return
    current = _current_rows(messages, uid)
    if not current or report_turn(agent, current[0]):
        return
    try:
        from psycopg import Error
    except ImportError:
        return
    from hermes_state_store import StoreUnavailableError

    try:
        _prepare(agent, home, messages, current)
    except (Error, StoreUnavailableError):
        logger.warning("Taste voice store unavailable while preparing the owner turn", exc_info=True)
        finish(agent, None)


def _activity_kind(current):
    """When the picker supplied no task type, compare turns using their first actual work tool."""
    for row in current:
        if row.get("role") == "assistant":
            for call in row.get("tool_calls") or ():
                name = (call.get("function") or {}).get("name")
                if name and name != "memory_recall":
                    return "tool:" + name
    return "text_reply"


def finish(agent, result):
    state = getattr(agent, "_taste_voice_turn", None)
    if not isinstance(state, _VoiceTurn) or state.turn_id != agent._current_turn_id:
        return
    from psycopg import Error
    from hermes_state_store import StoreUnavailableError
    from agent.taste_voice_store import measure, settle

    try:
        result = result if isinstance(result, dict) else {}
        text = result.get("final_response")
        current = _current_rows(result.get("messages") or [], state.source_uid)
        completed = bool(current and isinstance(text, str) and text.strip() and result.get("completed")
                         and not result.get("failed") and not result.get("interrupted") and not report_tools(current))
        changes = settle(state.home, state.turn_id, completed=completed) if state.claimed else ()
        if completed:
            if weekly := render_weekly(changes):
                from agent.turn_finalizer import public_pre_transform

                # The stream carried the original before any earlier output hook ran.
                original = result.get("pre_transform_response") if result.get("response_transformed") else text
                separator = "" if text.endswith("\n") else "\n"
                result.update(final_response=text + separator + weekly, response_transformed=True,
                              pre_transform_response=public_pre_transform(agent, original),
                              response_previewed=False, response_reused=False)
            measure(state.home, state.turn_id, state.task_kind or _activity_kind(current),
                    len(text), verification_steps(current), disliked=state.disliked)
    except (Error, StoreUnavailableError):
        logger.warning("Taste voice store unavailable while settling the owner turn", exc_info=True)
    finally:
        agent._taste_voice_turn = None


@contextmanager
def after_output(agent, result):
    try:
        yield
    except BaseException:
        finish(agent, None)
        raise
    else:
        finish(agent, result)
