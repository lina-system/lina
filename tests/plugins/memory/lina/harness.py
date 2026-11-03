"""A home with ``memory.provider: lina-memory`` and ``context.engine: lcm-x`` driven through the real ``AIAgent``.

The model client is stubbed, Honcho's SDK client is a recording fake (``FakeHoncho``), LCM-X writes its real
``lcm.db`` and the message store is the test Postgres (tests/_fixtures/memory_store.py). Nothing reaches the
network.
"""

from __future__ import annotations

import contextlib
import json
import sqlite3
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Optional
from unittest.mock import MagicMock, patch

HONCHO_CONFIG = {
    "apiKey": "test-key", "enabled": True, "workspace": "lina-test", "peerName": "alice", "aiPeer": "lina-bot",
    "writeFrequency": "turn", "recallMode": "tools", "initOnSessionStart": True,
}


class FakeHoncho:
    """Honcho's SDK client, recording every message ``add_messages`` sends."""

    def __init__(self) -> None:
        self.written: list[dict[str, Any]] = []
        self._peers: dict[str, MagicMock] = {}
        self._sessions: dict[str, MagicMock] = {}

    def peer(self, peer_id: str, **_kwargs):
        if peer_id not in self._peers:
            peer = MagicMock(name=f"peer:{peer_id}")
            peer.id = peer_id
            peer.message.side_effect = lambda content, metadata=None, _p=peer_id: {
                "peer": _p, "content": content, "metadata": metadata}
            self._peers[peer_id] = peer
        return self._peers[peer_id]

    def session(self, session_id: str, **_kwargs):
        if session_id not in self._sessions:
            session = MagicMock(name=f"session:{session_id}")
            session.id = session_id
            session.add_messages.side_effect = lambda messages, _s=session_id: self.written.extend(
                {**m, "session": _s} for m in messages)
            session.context.side_effect = RuntimeError("no context in the fake")
            session.messages.return_value = []
            self._sessions[session_id] = session
        return self._sessions[session_id]

    def __getattr__(self, name: str):
        return MagicMock(name=name)


def write_home(path: Path, *, memory_provider: str = "lina-memory", honcho: Optional[dict] = None) -> Path:
    path.mkdir(parents=True, exist_ok=True)
    config = "context:\n  engine: lcm-x\n"
    if memory_provider:
        config += f"memory:\n  provider: {memory_provider}\n"
    (path / "config.yaml").write_text(config, encoding="utf-8")
    (path / "honcho.json").write_text(json.dumps(honcho or HONCHO_CONFIG), encoding="utf-8")
    return path


def make_agent(session_id: str, session_db, *, platform: str = "telegram", user_id: Optional[str] = "1001",
               gateway_session_key: Optional[str] = "agent:main:telegram:dm:1001", skip_memory: bool = False):
    from run_agent import AIAgent

    identity = {k: v for k, v in (("user_id", user_id), ("gateway_session_key", gateway_session_key)) if v}
    with (
        patch("model_tools.get_tool_definitions", return_value=[]),
        patch("model_tools.check_toolset_requirements", return_value={}),
        patch("agent.process_bootstrap.OpenAI"),
    ):
        return AIAgent(api_key="test-key-1234567890", base_url="https://openrouter.ai/api/v1", quiet_mode=True,
                       skip_context_files=True, session_id=session_id, platform=platform, session_db=session_db,
                       skip_memory=skip_memory, **identity)


def _response(content: Optional[str], tool_calls=None):
    message = SimpleNamespace(content=content, tool_calls=tool_calls, reasoning_content=None, reasoning=None)
    return SimpleNamespace(choices=[SimpleNamespace(message=message, finish_reason="tool_calls" if tool_calls else "stop")],
                           model="test/model", usage=None)


def run_turn(agent, user: str, reply: str, history=None, *, tool_round: Optional[str] = None,
             display_kind: Optional[str] = None, turn_author: Optional[dict] = None) -> dict:
    responses = []
    if tool_round:
        call = SimpleNamespace(id=f"call_{tool_round}", type="function",
                               function=SimpleNamespace(name="lookup_note", arguments=json.dumps({"q": tool_round})))
        responses.append(_response(None, [call]))
    responses.append(_response(reply))
    agent.client = MagicMock()
    agent.client.chat.completions.create.side_effect = responses
    agent._cached_system_prompt = "You are helpful."
    kwargs: dict[str, Any] = {}
    if display_kind:
        kwargs["persist_user_display_kind"] = display_kind
    if turn_author:
        kwargs["turn_author"] = turn_author
    result = agent.run_conversation(user, conversation_history=history, **kwargs)
    assert result.get("final_response") == reply, result
    return result


def drain(agent) -> None:
    assert agent._memory_manager.flush_pending(timeout=60)
    for provider in agent._memory_manager.providers:
        getattr(provider, "_drain", lambda: None)()


def lcm_rows(home: Path) -> list[tuple]:
    with contextlib.closing(sqlite3.connect(home / "lcm.db")) as con:
        return con.execute("SELECT session_id, conversation_id, role, content FROM messages ORDER BY store_id").fetchall()
