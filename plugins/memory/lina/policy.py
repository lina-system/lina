"""lina-memory's policy table (``policy.yaml``): the route a finished turn arrived on, and per route whether
it goes to LCM and to Honcho and as whose words (lina#20). The route comes from where the turn arrived,
never from its text."""

from __future__ import annotations

from dataclasses import dataclass
from functools import lru_cache
from pathlib import Path
from typing import Any, Mapping, Optional, Sequence

import hermes_yaml

POLICY_PATH = Path(__file__).with_name("policy.yaml")
_SIGNAL_FIELDS = ("agent_context", "platform", "display_kind", "compressor_row", "author_is_bot")
# Producer-owned flags of rows the context compressor writes (agent/context_compressor.py,
# agent/message_metadata.py); SessionDB keeps both, so a resumed row still carries them.
_COMPRESSED_SUMMARY_KEY = "_compressed_summary"
_INFLIGHT_RESTATEMENT_KEY = "compaction_inflight_task_restatement"


@dataclass(frozen=True)
class Rule:
    route: str
    lcm: bool
    honcho: bool
    words_of: str


@dataclass(frozen=True)
class TurnSignals:
    agent_context: str = ""
    platform: str = ""
    display_kind: str = ""
    compressor_row: bool = False
    author_is_bot: bool = False


class Policy:
    def __init__(self, table: Mapping[str, Any]) -> None:
        self._routes = {name: Rule(name, bool(row["lcm"]), bool(row["honcho"]), str(row["words_of"]))
                        for name, row in table["routes"].items()}
        self._match = [dict(row) for row in table["match"]]
        self._roles = dict(table["roles"])
        self.replaces = [dict(row) for row in table.get("replaces", ())]
        unknown = {row["route"] for row in self._match} | set(self._roles.values())
        unknown |= {row["route"] for row in self.replaces}
        unknown -= set(self._routes)
        if unknown:
            raise ValueError(f"policy.yaml names routes it does not define: {sorted(unknown)}")
        if set(self._match[-1]) != {"route"}:
            raise ValueError("policy.yaml: the last match row must match every turn")
        for row in self._match:
            extra = set(row) - {"route", *_SIGNAL_FIELDS}
            if extra:
                raise ValueError(f"policy.yaml: unknown match fields {sorted(extra)}")

    def route_for(self, signals: TurnSignals) -> str:
        for row in self._match:
            if all(getattr(signals, field) == value for field, value in row.items() if field != "route"):
                return row["route"]
        raise AssertionError("unreachable: the last match row matches every turn")

    def rule(self, route: str) -> Rule:
        return self._routes[route]

    def rule_for_role(self, role: str) -> Optional[Rule]:
        route = self._roles.get(role)
        return self._routes[route] if route else None

    @property
    def routes(self) -> list[str]:
        return list(self._routes)


@lru_cache(maxsize=1)
def load_policy(path: Path = POLICY_PATH) -> Policy:
    return Policy(hermes_yaml.safe_load(path.read_text(encoding="utf-8-sig")))


def is_compressor_row(message: Mapping[str, Any]) -> bool:
    metadata = message.get("display_metadata")
    return bool(message.get(_COMPRESSED_SUMMARY_KEY)) or (
        isinstance(metadata, Mapping) and bool(metadata.get(_INFLIGHT_RESTATEMENT_KEY)))


def turn_user_index(messages: Sequence[Any]) -> Optional[int]:
    """Index of the row the turn's user side arrived as: the last user row of this turn the compressor did not
    write, else (the turn's input is the compressor's own row) the last compressor-written one. This turn's rows
    start after the previous turn's final reply, the last assistant row without tool calls before the tail."""
    end = len(messages)
    while end and isinstance(messages[end - 1], Mapping) and messages[end - 1].get("role") == "assistant":
        end -= 1
    fallback = None
    for index in range(end - 1, -1, -1):
        message = messages[index]
        if not isinstance(message, Mapping):
            continue
        if message.get("role") == "assistant" and not message.get("tool_calls"):
            break
        if message.get("role") != "user":
            continue
        if not is_compressor_row(message):
            return index
        if fallback is None:
            fallback = index
    return fallback


def signals_for_turn(messages: Optional[Sequence[Any]], *, agent_context: str = "", platform: str = "",
                     turn_author: Optional[Mapping[str, Any]] = None) -> TurnSignals:
    rows = list(messages or ())
    index = turn_user_index(rows)
    row: Mapping[str, Any] = rows[index] if index is not None else {}
    return TurnSignals(
        agent_context=str(agent_context or ""), platform=str(platform or ""),
        display_kind=str(row.get("display_kind") or ""), compressor_row=bool(row) and is_compressor_row(row),
        author_is_bot=bool(isinstance(turn_author, Mapping) and turn_author.get("is_bot")),
    )
