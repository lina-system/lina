"""The LINA app's exchange reaches only the owner's next DM turn with the agent on a messaging platform, as model-facing
context: the stored user row and the memory sync keep what the owner typed (lina#167)."""

from __future__ import annotations

from pathlib import Path
from types import SimpleNamespace

import pytest

from gateway.app_talk_carry import carry_into_dm_turn, write_carry
from gateway.config import GatewayConfig, HomeChannel, Platform, PlatformConfig
from gateway.session import SessionSource
from hermes_constants import get_routing_process_hermes_home

NOTE = {"item": "T1", "title": "기기 1 점검", "message": "파랑", "reply": "알겠어요, 파랑으로 할게요"}
OWNER_CHAT = "1"


@pytest.fixture
def home():
    return Path(get_routing_process_hermes_home())


def _runner(home, chat_id=OWNER_CHAT):
    platforms = {Platform.TELEGRAM: PlatformConfig(
        enabled=True, home_channel=HomeChannel(Platform.TELEGRAM, chat_id, "Owner") if chat_id else None)}
    return SimpleNamespace(_resolve_profile_home_for_source=lambda source: home,
                           config=GatewayConfig(platforms=platforms), session_store=SimpleNamespace(_entries={}))


def _source(chat_id=OWNER_CHAT, *, platform=Platform.TELEGRAM, chat_type="dm"):
    return SessionSource(platform=platform, user_id=chat_id, chat_id=chat_id, chat_type=chat_type)


def _turn(runner, source, message):
    from gateway.run_turn_runner import TurnRunner
    ctx = SimpleNamespace(source=source, message=message, persist_user_message=None, persist_user_timestamp=None,
                          inbound_message_id=None, history=[], session_key="agent:main:telegram:dm:1")
    persist, _ts = TurnRunner(runner, ctx)._prepare_turn_message([])
    return ctx.message, persist


@pytest.mark.parametrize("platform, chat_type", [
    (Platform.TELEGRAM, "group"), (Platform.API_SERVER, "dm"), (Platform.LOCAL, "dm"), (Platform.WEBHOOK, "dm")])
def test_a_group_or_non_messaging_turn_leaves_the_note_waiting(home, platform, chat_type):
    write_carry(home, NOTE)
    assert carry_into_dm_turn(_runner(home), _source(platform=platform, chat_type=chat_type), "hi") == "hi"
    assert "Owner: 파랑" in carry_into_dm_turn(_runner(home), _source(), "hi")


def test_a_dm_from_another_user_leaves_the_note_for_the_owner(home):
    write_carry(home, NOTE)
    assert carry_into_dm_turn(_runner(home), _source("2"), "누구세요") == "누구세요"
    owners = carry_into_dm_turn(_runner(home), _source(), "hi")
    assert "Owner: 파랑" in owners and carry_into_dm_turn(_runner(home), _source(), "again") == "again"


def test_without_a_home_channel_no_dm_takes_the_note(home):
    write_carry(home, NOTE)
    assert carry_into_dm_turn(_runner(home, chat_id=None), _source(), "hi") == "hi"
    assert "Owner: 파랑" in carry_into_dm_turn(_runner(home), _source(), "hi")


def test_notes_arrive_oldest_first_and_once(home):
    write_carry(home, NOTE)
    write_carry(home, {**NOTE, "message": "빨강"})
    first = carry_into_dm_turn(_runner(home), _source(), "hi")
    assert first.index("Owner: 파랑") < first.index("Owner: 빨강") and first.endswith("\n\nhi")
    assert carry_into_dm_turn(_runner(home), _source(), "again") == "again"


def test_the_stored_row_and_the_memory_sync_get_only_what_the_owner_typed(home, monkeypatch):
    """The owner's DM turn takes the note; the model reads it, while the persisted user row and the turn handed to
    the memory provider are the owner's text alone, through the real ``AIAgent.run_conversation``."""
    from agent.memory_manager import MemoryManager
    from agent.memory_provider import MemoryProvider
    from run_agent import AIAgent

    class Recorder(MemoryProvider):
        synced: list = []
        name = "recorder"

        def initialize(self, session_id="", **kw): pass
        def is_available(self): return True
        def system_prompt_block(self): return ""
        def get_tool_schemas(self): return []
        def prefetch(self, query, *, session_id=""): return ""
        def queue_prefetch(self, query, *, session_id=""): pass
        def sync_turn(self, user_content, assistant_content, *, session_id="", messages=None, **kw):
            self.synced.append(user_content)
        def handle_tool_call(self, tool_name, args, **kw): return "{}"

    seen_by_model = []

    class Completions:
        def create(self, **kw):
            seen_by_model.append(kw["messages"][-1]["content"])
            return SimpleNamespace(choices=[SimpleNamespace(
                message=SimpleNamespace(content="네, 파랑으로 했어요", tool_calls=None, reasoning=None,
                                        reasoning_content=None), finish_reason="stop")], usage=None, model="test-model")

    monkeypatch.setattr("agent.process_bootstrap.OpenAI",
                        lambda **_kw: SimpleNamespace(chat=SimpleNamespace(completions=Completions())))
    monkeypatch.setattr("model_tools.get_tool_definitions", lambda *a, **k: [])
    write_carry(home, NOTE)
    message, persist = _turn(_runner(home), _source(), "아까 말한 거 했어?")
    assert persist == "아까 말한 거 했어?" and "You replied:" in message

    agent = AIAgent(model="test-model", api_key="k", base_url="http://localhost:1/v1", platform="telegram",
                    max_iterations=2, quiet_mode=True, skip_memory=True)
    agent._disable_streaming = True
    manager = MemoryManager()
    manager.add_provider(Recorder())
    agent._memory_manager = manager
    result = agent.run_conversation(message, persist_user_message=persist)
    manager.flush_pending(timeout=5.0)

    assert "You replied: 알겠어요, 파랑으로 할게요" in str(seen_by_model[-1])
    assert Recorder.synced == ["아까 말한 거 했어?"]
    users = [m["content"] for m in result["messages"] if m.get("role") == "user"]
    assert users == ["아까 말한 거 했어?"]
