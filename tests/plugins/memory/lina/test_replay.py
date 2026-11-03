"""lina#20: replaying 1,000 recorded-shape turns through the real ``AIAgent`` with ``memory.provider: lina-memory``
and ``context.engine: lcm-x``. Each turn lands once per engine, as the policy table says, with the same person,
agent, conversation and message ids on both sides; LCM-X's own per-turn ingest hook stays off.

The turns are generated (no recorded bot data): a person's channel turns, agent replies, tool rounds, cron
notices, bot relays, gateway-internal notices, delegation and background notices, a session switch and a
compression boundary.
"""

from __future__ import annotations

import contextlib
import re
import sqlite3
from collections import Counter
from types import SimpleNamespace
from unittest.mock import patch

import pytest

from tests.plugins.memory.lina.harness import drain, make_agent, run_turn, write_home

TURNS = 1000
BOT = {"id": "bot:scout", "name": "Scout", "is_bot": True}
# kind -> (how it arrives, the route the policy table gives it)
KINDS = {
    "person": ({}, "person_channel"),
    "tool": ({"tool_round": True}, "person_channel"),
    "gateway": ({"display_kind": "internal_notification"}, "gateway_notice"),
    "worker": ({"display_kind": "async_delegation_complete"}, "worker_event"),
    "background": ({"display_kind": "process_complete"}, "background_notice"),
    "relay": ({"turn_author": BOT}, "bot_relay"),
    "cron": ({"cron": True}, "cron_notice"),
}
_CYCLE = ["person", "tool", "person", "gateway", "person", "worker", "relay", "person", "background", "cron",
          "person", "tool", "relay", "person", "gateway", "person", "cron", "person", "worker", "person"]
SESSION_SWITCH_AT = 400
COMPRESSION_AT = 700
_TEXT = {
    "gateway": "[IMPORTANT: Background process {n} matched watch pattern \"ERROR\"",
    "worker": "[ASYNC DELEGATION COMPLETE — deleg_{n}] the research subagent finished",
    "background": "Background process {n} finished with exit code 0",
    "relay": "Scout relays the build status for item {n}",
    "cron": "Cron job digest {n}: three new mails",
}


def recorded_turns() -> list[SimpleNamespace]:
    turns = []
    for n in range(TURNS):
        kind = _CYCLE[n % len(_CYCLE)]
        user = _TEXT.get(kind, "person question {n} about the garden plan").format(n=f"{n:04d}")
        turns.append(SimpleNamespace(n=n, kind=kind, user=f"{user} #t{n:04d}", reply=f"reply to #t{n:04d}",
                                     route=KINDS[kind][1], **KINDS[kind][0]))
    return turns


def _fake_summary_model(**kwargs):
    system = "\n".join(m["content"] for m in kwargs["messages"] if m["role"] == "system")
    nonce = re.search(r'<lcm-summary nonce="([0-9a-f]+)">', system).group(1)
    text = (f'<lcm-summary nonce="{nonce}">\nThe person asked numbered garden questions; each was answered.\n'
            "Expand for details about: the numbered garden questions\n</lcm-summary>")
    return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=text))])


def _tag(text) -> str | None:
    found = re.search(r"#t\d{4}", str(text or ""))
    return found.group(0) if found else None


@pytest.fixture
def home(tmp_path, monkeypatch, lina_env):
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def _replay(home, fake_honcho):
    from hermes_state import SessionDB

    db = SessionDB(home / "state.db")
    agent, history, session = make_agent("replay-1", db), [], "replay-1"
    cron_agent, cron_history = make_agent("cron-1", db, platform="cron", user_id=None, gateway_session_key=None), []
    transcripts = {}
    for turn in recorded_turns():
        if turn.n == SESSION_SWITCH_AT:
            drain(agent)
            transcripts[session] = history
            agent.shutdown_memory_provider(history)
            agent, history, session = make_agent("replay-2", db), [], "replay-2"
        if turn.n == COMPRESSION_AT:
            drain(agent)
            engine = agent.context_compressor
            with patch("agent.auxiliary_client.call_llm", side_effect=_fake_summary_model):
                compacted = engine.compress([{"role": "system", "content": "You are helpful."}, *history],
                                            current_tokens=engine.context_length, force=True)
            assert any("Expand for details about" in str(m.get("content")) for m in compacted)
            history = [m for m in compacted if m.get("role") != "system"]
        if getattr(turn, "cron", False):
            cron_history = run_turn(cron_agent, turn.user, turn.reply, cron_history)["messages"]
            continue
        result = run_turn(agent, turn.user, turn.reply, history, tool_round=f"{turn.n:04d}" if getattr(
            turn, "tool_round", False) else None, display_kind=getattr(turn, "display_kind", None),
            turn_author=getattr(turn, "turn_author", None))
        history = result["messages"]
    for live, rows in ((agent, history), (cron_agent, cron_history)):
        drain(live)
        live.shutdown_memory_provider(rows)
    transcripts[session] = history
    transcripts["cron-1"] = cron_history
    return transcripts


def test_a_thousand_recorded_turns_land_once_per_engine_as_the_policy_table_says(home, lina_env, memory_store_sql):
    turns = recorded_turns()
    _replay(home, lina_env)
    by_tag = {f"#t{t.n:04d}": t for t in turns}
    to_honcho = {tag for tag, t in by_tag.items() if t.route in {"person_channel", "bot_relay"}}

    with contextlib.closing(sqlite3.connect(home / "lcm.db")) as con:
        lcm = con.execute("SELECT store_id, session_id, conversation_id, role, content FROM messages").fetchall()
        bindings = con.execute("SELECT uid, store_id FROM host_uid_bindings WHERE kind = 'canonical'").fetchall()

    # LCM: every turn's user row and reply, of every route, exactly once.
    lcm_counts = Counter((_tag(content), role) for _, _, _, role, content in lcm if role in {"user", "assistant"}
                         and content and _tag(content))
    assert {key for key in lcm_counts} == {(tag, role) for tag in by_tag for role in ("user", "assistant")}
    assert {key: count for key, count in lcm_counts.items() if count != 1} == {}

    # Honcho: exactly the turns the table lets in, each user text and reply once, nothing else.
    honcho_counts = Counter((_tag(m["content"]), m["metadata"]["lina_route"]) for m in lina_env.written)
    assert {tag for tag, _ in honcho_counts} == to_honcho
    assert {key: count for key, count in honcho_counts.items() if count != 1} == {}
    for message in lina_env.written:
        turn = by_tag[_tag(message["content"])]
        if message["metadata"]["lina_route"] == "agent_reply":
            assert message["peer"] == "lina-bot"
        elif turn.route == "bot_relay":  # a bot's words: its own peer, never the person's
            assert message["peer"] != "1001" and message["metadata"]["lina_person"] is None
        else:
            assert message["peer"] == "1001" and message["metadata"]["lina_route"] == "person_channel"

    # The same ids on both sides: Honcho's uid is the memory.messages row with that text, LCM bound that uid to
    # its row with that text, and both rows carry the conversation Honcho was told.
    rows = {uid: (content, conversation) for uid, content, conversation in memory_store_sql(
        "SELECT uid, content, conversation_id FROM memory.messages")}
    lcm_by_store = {store_id: (session, conversation, content) for store_id, session, conversation, _, content in lcm}
    lcm_uid_rows = Counter(uid for uid, _ in bindings)
    keys = {(source, key): identity for source, key, identity in memory_store_sql(
        "SELECT source, key, identity_id FROM memory.identity_keys WHERE kind = 'conversation'")}
    for message in lina_env.written:
        meta = message["metadata"]
        content, conversation = rows[meta["lina_uid"]]
        assert _tag(content) == _tag(message["content"]) and conversation == meta["lina_conversation"]
        hermes_uid = meta["lina_uid"].rsplit(":", 1)[-1]
        assert lcm_uid_rows[hermes_uid] == 1
        store_id = dict(bindings)[hermes_uid]
        _, lcm_conversation, lcm_content = lcm_by_store[store_id]
        assert _tag(lcm_content) == _tag(message["content"])
        assert keys[("lcm_conversation", f"default:{lcm_conversation}")] == meta["lina_conversation"]
    assert {m["metadata"]["lina_person"] for m in lina_env.written if m["metadata"]["lina_person"]} == {
        memory_store_sql("SELECT identity_id FROM memory.identity_keys WHERE source = 'gateway_user'")[0][0]}

    # Every row of the replay has its conversation; the two sessions of the chat are one conversation.
    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE conversation_id IS NULL") == [(0,)]
    assert len({m["metadata"]["lina_conversation"] for m in lina_env.written}) == 1


def test_one_turn_is_one_lcm_ingest_handed_over_by_lina_memory(home, lina_env):
    from hermes_cli.plugins import get_plugin_manager
    from hermes_state import SessionDB
    from plugins.memory import import_provider_module

    agent = make_agent("one-ingest", SessionDB(home / "state.db"))
    lcm = import_provider_module("lina-memory", "lcm")
    taken, hand_turn = [], lcm.hand_turn

    def handing(history, *, session_id):
        taken.append(session_id)
        return hand_turn(history, session_id=session_id)
    engine_cls = type(agent.context_compressor)
    calls = []
    original = engine_cls.ingest

    def counting(self, messages):
        calls.append(len(messages))
        return original(self, messages)

    with patch.object(engine_cls, "ingest", counting), patch.object(lcm, "hand_turn", handing):
        run_turn(agent, "Remember the code word PELICAN-7731.", "Noted: PELICAN-7731.")
        handed_by_turn_end = list(calls)  # the hook ran inside the turn, which waited for it
        drain(agent)

    assert len(get_plugin_manager()._hooks.get("post_llm_call", [])) == 1  # LCM-X's hook stays registered
    assert taken == ["one-ingest"]  # ...and handed this agent's turn to lina-memory
    # One ingest, finished before the turn returned, where LCM-X's own hook runs: never queued behind it.
    assert handed_by_turn_end == calls == [2]
