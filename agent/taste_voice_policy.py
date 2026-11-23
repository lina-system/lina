"""Report boundaries, expression, and quality comparisons for agent tastes."""

from __future__ import annotations

import json
import re
import unicodedata
from collections.abc import Collection, Iterable, Mapping
from pathlib import PurePosixPath
from statistics import median
from typing import Final, Literal, Protocol, TypedDict

from agent.delegation_context import owned_kanban_task
from agent.skill_commands import skills_loaded_by_scaffold
from agent.verification_evidence import _split_shell_segments

_REPORT_SKILLS: Final = frozenset({"report-pages"})
_REPORT_TOOLS: Final = frozenset({"kanban_complete", "kanban_request_review"})
_TASTE_WORDS: Final = {"like": "좋아함", "dislike": "싫어함", "indifferent": "선호 없음"}
_OPPOSITE: Final = {"like": "dislike", "dislike": "like"}
_TARGET_END: Final = r"(?:에서|에게|으로|까지|부터|처럼|은|는|이|가|을|를|의|에|로|와|과|도|만)?(?!\w)"
_OWNER_STATEMENTS: Final = (
    ("like", r"(?:저는 |나는 |제가 |내가 )?{target}(?:을|를|은|는)? (?:좋아해|좋아해요|좋아합니다|선호해|선호해요|선호합니다)"),
    ("dislike", r"(?:저는 |나는 |제가 |내가 )?{target}(?:을|를|은|는)? (?:싫어해|싫어해요|싫어합니다)"),
    ("like", r"i (?:like|prefer) {target}"),
    ("dislike", r"i dislike {target}"),
)


class _ActiveTools(Protocol):
    @property
    def valid_tool_names(self) -> Collection[str]: ...


class _Function(TypedDict, total=False):
    name: str
    arguments: str


class _Call(TypedDict, total=False):
    function: _Function


class _Message(TypedDict, total=False):
    role: str
    content: str | list[dict]
    tool_calls: list[_Call]
    tool_name: str
    name: str
    tool_call_id: str


class QualityFlags(TypedDict, total=False):
    short_output: Literal[1]
    skipped_verification: int


def report_turn(agent: _ActiveTools, user_row: _Message) -> bool:
    """Recognize report intent known before the turn executes any tools."""
    if owned_kanban_task() and _REPORT_TOOLS.intersection(agent.valid_tool_names):
        return True
    return user_row.get("role") == "user" and bool(
        _REPORT_SKILLS.intersection(skills_loaded_by_scaffold(user_row.get("content")))
    )


def _report_command(command: str) -> bool:
    """Recognize direct report invocations, including a leading directory change."""
    for segment in _split_shell_segments(command):
        executable, *args = segment.tokens
        name = PurePosixPath(executable).name
        if name == "lina" and args[:2] == ["desk", "register"]:
            args = args[2:]
        elif name != "report-link":
            # Only continue past a simple cd. Other commands could introduce
            # nested shell syntax whose contents are not top-level invocations.
            if executable == "cd" and len(args) == 1 and segment.following_operator in {"&&", ";"}:
                if re.fullmatch(r"[\w./ ~-]+", args[0]):
                    continue
            return False
        return bool(args and args[0] and not args[0].startswith("-")
                    and "--help" not in args and "-h" not in args)
    return False


def report_tools(messages: Iterable[_Message]) -> bool:
    """Recognize reporting from current-turn assistant tool calls."""
    for row in messages:
        if row.get("role") != "assistant":
            continue
        for call in row.get("tool_calls") or ():
            function = call.get("function") or {}
            if function.get("name") in _REPORT_TOOLS:
                return True
            if function.get("name") == "terminal":
                match _json_payload(function.get("arguments")):
                    case {"command": str(command)} if _report_command(command):
                        return True
    return False


def _json_payload(content: str | list[dict] | None):
    if isinstance(content, str):
        try:
            return json.loads(content)
        except (ValueError, RecursionError):
            return None
    return None


def verification_steps(messages: Iterable[_Message]) -> tuple[str, ...]:
    """Count distinct attempted checks using canonical terminal receipts."""
    observed: dict[str, None] = {}
    for row in messages:
        if row.get("role") != "tool" or (row.get("tool_name") or row.get("name")) != "terminal":
            continue
        if not row.get("tool_call_id"):
            continue
        match _json_payload(row.get("content")):
            case {"verification_evidence": {
                "canonical_command": str(command),
                "status": "passed" | "failed",
                "kind": str(),
                "scope": str(),
            }} if command.strip():
                observed[command] = None
    return tuple(observed)


def quality_flags(
    output_chars: int,
    steps: Iterable[str],
    history: Collection[tuple[int, Iterable[str]]],
) -> QualityFlags:
    """Compare a disliked task with previous samples of the same task kind."""
    if not history:
        return {}
    flags: QualityFlags = {}
    if output_chars < median(length for length, _ in history):
        flags["short_output"] = 1
    omitted = {step for _, checks in history for step in checks}.difference(steps)
    if omitted:
        flags["skipped_verification"] = len(omitted)
    return flags


def _fold(text: str) -> str:
    return unicodedata.normalize("NFKC", text).casefold()


def _safe_label(label: str) -> bool:
    return bool(0 < len(label) <= 80 and label.isprintable() and not {"<", ">"}.intersection(_fold(label)))


def _taste_parts(line: str) -> tuple[str, str] | None:
    direction, _, raw_label = line.partition(":")
    label = raw_label.strip()
    if direction in _TASTE_WORDS and raw_label.isprintable() and _safe_label(label):
        return direction, label
    return None


def resolve_owner_preferences(
    owner_utterance: str,
    selected_tastes: Iterable[str],
    *,
    structured_preferences: Mapping[str, str] | None = None,
) -> dict[str, str]:
    """Match selected labels against trusted preferences or the owner's own words.

    The caller supplies independently authenticated, original owner text.
    Skill payloads, recall prose, and transcript rows of unknown origin are not
    owner utterances. Current explicit statements take precedence over memory.
    """
    remembered = {_fold(label): valence for label, valence in (structured_preferences or {}).items()}
    utterance = _fold(owner_utterance.strip())
    statements = re.split(r"(?<=[.!?。])\s+", utterance)
    # Quotes, code, and markup can carry a preference as an example or citation.
    plain = not any(char in utterance for char in '<>`"“”「」')
    preferences: dict[str, str] = {}
    for line in selected_tastes:
        if (parts := _taste_parts(line)) is None:
            continue
        _, label = parts
        target = _fold(label)
        if remembered.get(target) in _TASTE_WORDS:
            preferences[label] = remembered[target]
        if plain:
            for statement in statements:
                for valence, pattern in _OWNER_STATEMENTS:
                    if re.fullmatch(pattern.format(target=re.escape(target)) + r"[.!。！]?", statement):
                        preferences[label] = valence
    return preferences


def voice_inputs(
    query: str,
    selected_tastes: Iterable[str],
    *,
    owner_preferences: Mapping[str, str] | None = None,
    task_targets: Collection[str] = (),
) -> tuple[str | None, str | None]:
    """Pick one explicit disagreement and one classified disliked task."""
    query_key = _fold(query)
    task_keys = {_fold(target) for target in task_targets}
    preferences = {_fold(label): valence for label, valence in (owner_preferences or {}).items()}
    difference = disliked_task = None
    for line in selected_tastes:
        if (parts := _taste_parts(line)) is None:
            continue
        direction, label = parts
        target = _fold(label)
        if not re.search(r"(?<!\w)" + re.escape(target) + _TARGET_END, query_key):
            continue
        if difference is None and direction in _OPPOSITE and preferences.get(target) == _OPPOSITE[direction]:
            difference = f"{direction}: {label}"
        if disliked_task is None and direction == "dislike" and target in task_keys:
            disliked_task = label
    return difference, disliked_task


def render_weekly(candidates: Iterable[str]) -> str:
    """Return a single ordinary-reply line for up to three approved changes."""
    descriptions = {"like": "좋아해요", "dislike": "별로 좋아하지 않아요", "indifferent": "특별한 선호가 없어요"}
    changes = []
    for line in candidates:
        if parts := _taste_parts(line):
            direction, label = parts
            changes.append(f"‘{label}’ 쪽은 {descriptions[direction]}.")
            if len(changes) == 3:
                break
    return "이번 주에 달라진 제 취향이에요. " + " ".join(changes) if changes else ""


def render_turn_instructions(
    difference: str | None = None, disliked_task: str | None = None,
) -> str:
    """Render guidance using only inputs that have an expression reservation."""
    lines = []
    if difference and (parts := _taste_parts(difference)) and parts[0] in _OPPOSITE:
        direction, label = parts
        lines.append(
            f"‘{label}’에 대한 제 취향({_TASTE_WORDS[direction]})은 사용자의 선호와 달라요. "
            "필요하면 답변에서 한 문장으로만 말하고, 결과물에는 사용자의 선호를 따르세요."
        )
    if disliked_task and _safe_label(disliked_task):
        lines.append(
            f"‘{disliked_task}’에 대한 제 취향은 답변에서 한 문장으로만 말할 수 있어요. "
            "평소와 같은 분량과 검증 기준을 지키세요."
        )
    if lines:
        lines.append("작업 보고에는 취향을 섞지 말고, 사실과 검증 결과를 그대로 전달하세요.")
    return "\n".join(lines)
