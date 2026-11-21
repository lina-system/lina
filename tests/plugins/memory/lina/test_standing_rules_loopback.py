"""Regression for #774. Check frozen standing rules on the actual cache wire."""

from __future__ import annotations

import hashlib
import json
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta
from importlib.util import find_spec
from itertools import pairwise
from pathlib import Path
from typing import TYPE_CHECKING, Final, Iterator, TypedDict

import pytest

from tests.plugins.memory.lina import cache_server
from tests.plugins.memory.lina.cache_load import _configure
from tests.plugins.memory.lina.harness import drain, write_home

if TYPE_CHECKING:
    from run_agent import AIAgent

START: Final = datetime(2026, 11, 21, 9, 0, tzinfo=UTC)
INITIAL_RULES: Final = (
    "- [2026-11-19] Say when a garden estimate is approximate.",
    "- [2026-11-20] Use metric units in planting plans.",
    "- [2026-11-20] Put seed counts beside each variety.",
    "Filter rules: 1. Ask to list them.",
)
NEW_RULE: Final = "- [2026-11-21] Mark seeds that need cold stratification."
USERS: Final = (
    "Sketch a planting plan for a small courtyard garden.",
    "Which vegetables would fit in the sunny raised bed?",
    "What could I grow along the shaded wall?",
    "Choose three herbs that work in pots.",
    "Which flower seeds could I sow before winter?",
    "Outline a seed order for the spring beds.",
    "What can I start indoors in February?",
    "Which seedlings need a larger pot first?",
    "How far apart should I plant the peas?",
    "Suggest a support for the climbing beans.",
    "How should I prepare the soil for carrots?",
    "Where should the watering can stay?",
    "Which flowers would attract pollinators?",
    "How can I protect seedlings from a late frost?",
    "What can I harvest first?",
    "Name two crops I could sow again in summer.",
    "Which plants should I grow in a deeper container?",
    "How should I record germination dates?",
    "What should I check before transplanting?",
    "Summarize the next steps for the courtyard.",
)


class Message(TypedDict):
    role: str
    content: str


class Request(TypedDict):
    messages: list[Message]


@contextmanager
def _fixture_agent(tmp_path: Path, monkeypatch: pytest.MonkeyPatch, url: str) -> Iterator[AIAgent]:
    from hermes_state import SessionDB
    from plugins.memory.lina import LinaMemoryProvider
    from run_agent import AIAgent

    home = write_home(tmp_path / "home")
    (home / "SOUL.self.md").write_text("Use the courtyard notebook for observations.\n", encoding="utf-8")
    _configure(home, url, "  prefix_fingerprints: true\n")
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(LinaMemoryProvider, "prefetch", lambda self, query, session_id="": "")
    monkeypatch.setattr(
        LinaMemoryProvider, "session_card_block", lambda self: "The courtyard has one sunny bed."
    )
    # These are host inputs to the real builder. Keeping them fixed permits raw
    # byte comparison across source trees and separate fixture homes.
    monkeypatch.setattr("agent.system_prompt.get_default_hermes_root", lambda: Path("/fixture/profile"))
    monkeypatch.setattr(
        "agent.prompt_builder.build_environment_hints", lambda: "Current working directory: /fixture/garden"
    )
    monkeypatch.setattr("agent.coding_context.build_coding_workspace_block", lambda cwd=None: "")
    monkeypatch.setattr("tools.env_probe.get_environment_probe_line", lambda: "")

    with SessionDB(db_path=home / "state.db") as db:
        agent = AIAgent(
            api_key="loopback", base_url=url, provider="custom", model="m", quiet_mode=True,
            skip_context_files=True, session_id="20261121_090000_standingrules", platform="cli",
            session_db=db, skip_background_review=True, enabled_toolsets=["skills", "memory", "todo"],
        )
        agent.session_start = START
        try:
            yield agent
        finally:
            drain(agent)


def _system_bytes(request: Request) -> bytes:
    systems = [message["content"] for message in request["messages"] if message["role"] == "system"]
    assert len(systems) == 1
    return systems[0].encode()


def test_rule_change_leaves_system_frozen_and_adds_one_current_user_note(tmp_path, monkeypatch, lina_env):
    rules = INITIAL_RULES
    # The same test drives the immutable baseline, where the reader does not
    # exist yet. Its standing-rule assertions must fail there after collecting usage.
    if find_spec("agent.standing_rules_section") is not None:
        monkeypatch.setattr("agent.standing_rules_section.read_rule_lines", lambda agent: rules)

    now = START
    requests: list[Request] = []
    wires: list[bytes] = []
    original_wire = cache_server.wire

    def record_wire(body: Request) -> bytes:
        wire = original_wire(body)
        requests.append(body)
        wires.append(wire)
        return wire

    monkeypatch.setattr("hermes_time.now", lambda: now)
    monkeypatch.setattr(cache_server, "wire", record_wire)
    with cache_server.cache_model(lambda: now, "standing-rules-loopback") as server:
        with _fixture_agent(tmp_path, monkeypatch, server.url) as agent:
            server.compaction_count = lambda: agent.context_compressor.compression_count
            history = None
            for turn, user in enumerate(USERS, start=1):
                result = agent.run_conversation(user, conversation_history=history)
                assert result["final_response"] == f"reply {turn}: noted."
                history = result["messages"]
                if turn == 5:
                    rules = (*rules[:-1], NEW_RULE, rules[-1])
                now += timedelta(minutes=1)

    assert len(requests) == len(server.usage) == len(USERS)
    systems = [_system_bytes(request) for request in requests]
    users = [[message["content"] for message in request["messages"] if message["role"] == "user"]
             for request in requests]
    signatures = [
        {
            "turn": turn,
            "wire_bytes": len(wire),
            "wire_sha256": hashlib.sha256(wire).hexdigest(),
            "system_bytes": len(system),
            "system_sha256": hashlib.sha256(system).hexdigest(),
            "compactions": compactions,
            "current_user": user_rows[-1],
            "new_rule_user_rows": [index for index, text in enumerate(user_rows, 1) if NEW_RULE in text],
        }
        for turn, (wire, system, compactions, user_rows) in enumerate(
            zip(wires, systems, server.compactions, users, strict=True), 1
        )
    ]
    for filename, records in (("usage.jsonl", server.usage), ("request-signatures.jsonl", signatures)):
        (tmp_path / filename).write_text("".join(json.dumps(row) + "\n" for row in records), encoding="utf-8")

    assert server.compactions == [0] * len(USERS)
    assert len(set(systems)) == 1
    assert NEW_RULE.encode() not in systems[0]
    assert all(wire.startswith(previous) for previous, wire in pairwise(wires))
    assert [turn for turn, rows in enumerate(users, 1) if NEW_RULE in rows[-1]] == [6]
    assert all(line.encode() in systems[0] for line in INITIAL_RULES)
    assert systems[0].index(b"## Plugin Context: owner-standing-rules") < systems[0].index(
        b"## Plugin Context: agent-self-note"
    )
    assert [len(rows) for rows in users] == list(range(1, len(USERS) + 1))
    for turn, (literal, rows) in enumerate(zip(USERS, users, strict=True), 1):
        if turn == 6:
            assert rows[-1].startswith(literal + "\n\n")
            assert rows[-1].count(NEW_RULE) == 1
        else:
            assert rows[-1] == literal
        assert [i for i, text in enumerate(rows, 1) if NEW_RULE in text] == ([6] if turn >= 6 else [])


def test_empty_standing_rules_keep_the_builder_output_without_a_rules_section(tmp_path, monkeypatch, lina_env):
    from agent.system_prompt import build_system_prompt

    if find_spec("agent.standing_rules_section") is not None:
        monkeypatch.setattr("agent.standing_rules_section.read_rule_lines", lambda agent: ())
    monkeypatch.setattr("hermes_time.now", lambda: START)
    with cache_server.cache_model(lambda: START, "standing-rules-empty") as server:
        with _fixture_agent(tmp_path, monkeypatch, server.url) as agent:
            system = build_system_prompt(agent).encode()

    # Compare this output byte for byte with the same test on the immutable
    # baseline. No expected prompt is derived from the new rules implementation.
    (tmp_path / "no-rule-system.bin").write_bytes(system)
    assert b"## Plugin Context: agent-self-note" in system
    assert b"## Plugin Context: owner-standing-rules" not in system
