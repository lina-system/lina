"""Owner-only taste context for the frozen section and the turn's recall result."""

from __future__ import annotations

from collections.abc import Iterator, Mapping
from contextlib import contextmanager
from contextvars import ContextVar
from pathlib import Path
from typing import TYPE_CHECKING, Any, Final

from agent.delegation_context import is_delegated_child_process_context, owned_kanban_task
from agent.memory_caller_scope import caller_scope
from agent.memory_recall_tool import RECALL_TOOL_NAME
from agent.safe_worker_policy import safe_worker_enabled
from agent.skill_commands import extract_user_instruction_from_skill_message

if TYPE_CHECKING:
    from run_agent import AIAgent

_OPEN: Final = "<owner-taste-picks>\n"
_CLOSE: Final = "\n</owner-taste-picks>"
_PROMPT_SCOPE: ContextVar[tuple[AIAgent, Path | None] | None] = ContextVar(
    "owner_taste_prompt_scope", default=None,
)


def owner_taste_home(agent: AIAgent) -> Path | None:
    """The admitted turn's home while its frozen prompt is being rendered."""
    scope = _PROMPT_SCOPE.get()
    return scope[1] if scope and scope[0] is agent else None


def admitted_source_row(messages, index: int, admitted: dict) -> dict | None:
    """A compressor can copy the admitted row or re-anchor to an older one."""
    if 0 <= index < len(messages):
        row = messages[index]
        if row is admitted or row.get("message_uid") == admitted["message_uid"]:
            return row
    return None


def request_taste_view(agent, prompt: str, messages: list[dict]) -> tuple[str, list[dict]]:
    if getattr(agent, "_owner_taste_allowed", False):
        return prompt, messages
    from agent.system_prompt import strip_core_tastes_from_prompt

    return strip_core_tastes_from_prompt(prompt), redact_owner_taste_rows(messages)


@contextmanager
def owner_taste_prompt_scope(agent: AIAgent, home: Path | None) -> Iterator[None]:
    token = _PROMPT_SCOPE.set((agent, home))
    try:
        yield
    finally:
        _PROMPT_SCOPE.reset(token)


def resolve_owner_taste_home(
    agent: AIAgent, row: Mapping[str, Any], author: Mapping[str, Any] | None,
) -> Path | None:
    """Resolve the LINA home only for an admitted owner turn in its provider's scope."""
    manager = getattr(agent, "_memory_manager", None)
    platform = getattr(agent, "platform", None)
    if manager is None or not platform:
        return None
    if (
        getattr(agent, "_turn_origin", None)
        or getattr(agent, "_memory_write_origin", "assistant_tool") != "assistant_tool"
        or getattr(agent, "_delegate_depth", 0)
        or getattr(agent, "_persist_disabled", False)
        or safe_worker_enabled()
        or is_delegated_child_process_context()
        or owned_kanban_task()
    ):
        return None

    from gateway.session_api_turn import api_execution

    # The API route's author is an attribution label supplied by the caller.
    if api_execution.get() is not None:
        return None
    get_provider = getattr(manager, "get_provider", None)
    provider = get_provider("lina-memory") if get_provider else None
    is_owner = getattr(provider, "is_owner_turn", None)
    bound = getattr(manager, "_bound_scope", None)
    if is_owner is None or not bound:
        return None
    scope = caller_scope(bound, "owner taste")
    if scope and is_owner(row, author):
        return Path(scope["hermes_home"])
    return None


def owner_turn_taste_suffix(home: Path, query: str) -> str:
    """Render this turn's matching tastes as a removable suffix of its recall result."""
    query = (extract_user_instruction_from_skill_message(query) or "").strip()
    if not query:
        return ""

    try:
        from psycopg import Error as PostgresError
    except ImportError:
        return ""

    from hermes_cli.heart.taste_context import pick_lines
    from hermes_state_store import StoreUnavailableError

    try:
        lines = pick_lines(home, query)
    except (StoreUnavailableError, PostgresError):
        return ""
    if not lines:
        return ""
    body = "\n".join(lines)
    # Row data cannot close the runtime-owned boundary.
    for tag in ("<owner-taste-picks>", "</owner-taste-picks>", "<memory-context>", "</memory-context>"):
        body = body.replace(tag, "&lt;" + tag[1:])
    return f"{_OPEN}This agent's own tastes for this turn:\n{body}{_CLOSE}"


def strip_owner_taste_suffix(content: str) -> str:
    """Remove the generated terminal block, leaving any ordinary recall bytes intact."""
    if not content.endswith(_CLOSE):
        return content
    if content.startswith(_OPEN):
        return ""
    recall, separator, _ = content.rpartition(f"\n\n{_OPEN}")
    if separator and recall.startswith("<memory-context>\n") and recall.endswith("\n</memory-context>"):
        return recall
    return content


def redact_owner_taste_rows(messages: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Return a request view with only the generated taste suffixes removed."""
    redacted: list[dict[str, Any]] = []
    for row in messages:
        content = row.get("content")
        if (
            row.get("role") == "tool" and (row.get("name") or row.get("tool_name")) == RECALL_TOOL_NAME
            and isinstance(content, str) and (recall := strip_owner_taste_suffix(content)) != content
            and redacted and _is_generated_recall_call(redacted[-1], row.get("tool_call_id"))
        ):
            if not recall:
                redacted.pop()
                continue
            row = {**row, "content": recall}
        redacted.append(row)
    return redacted


def _is_generated_recall_call(row: Mapping[str, Any], call_id: str | None) -> bool:
    calls = row.get("tool_calls") or ()
    if not call_id or row.get("role") != "assistant" or row.get("content") or len(calls) != 1:
        return False
    call = calls[0]
    function = call.get("function") or {}
    return (
        call.get("id") == call_id and function.get("name") == RECALL_TOOL_NAME
        and function.get("arguments") == "{}"
    )
