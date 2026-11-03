"""lina#21: the runtime's memory_recall round must name a tool the request defines, or strict providers
reject it (Anthropic tool_use, Responses function_call). Builds each provider's request body, no network."""

import pytest

from agent.memory_manager import MemoryManager
from agent.memory_manager_tools import inject_memory_provider_tools
from agent.memory_provider import MemoryProvider
from agent.transports import get_transport
from agent.turn_context import recall_tool_rows


class Remembering(MemoryProvider):
    @property
    def name(self) -> str:
        return "remembering"

    def is_available(self) -> bool:
        return True

    def initialize(self, session_id: str, **kwargs) -> None:
        pass

    def get_tool_schemas(self):
        return []


class Agent:
    session_id = "sess-tools"
    enabled_toolsets = None
    disabled_toolsets = None

    def __init__(self):
        self.tools = [{"type": "function", "function": {
            "name": "todo", "description": "Todo list.", "parameters": {"type": "object", "properties": {}}}}]
        self.valid_tool_names = {"todo"}
        self._memory_manager = MemoryManager()
        self._memory_manager.add_provider(Remembering())
        inject_memory_provider_tools(self)


def _names(mode, kwargs):
    if mode == "anthropic_messages":
        defined = {t["name"] for t in kwargs.get("tools") or []}
        called = {b["name"] for m in kwargs["messages"] if isinstance(m["content"], list)
                  for b in m["content"] if b.get("type") == "tool_use"}
    elif mode == "codex_responses":
        defined = {t["name"] for t in kwargs.get("tools") or []}
        called = {i["name"] for i in kwargs["input"] if i.get("type") == "function_call"}
    else:
        defined = {t["function"]["name"] for t in kwargs.get("tools") or []}
        called = {c["function"]["name"] for m in kwargs["messages"] for c in m.get("tool_calls") or []}
    return defined, called


@pytest.mark.parametrize("mode", ["anthropic_messages", "chat_completions", "codex_responses"])
def test_every_tool_call_in_the_history_names_a_defined_tool(mode):
    agent = Agent()
    messages = [{"role": "system", "content": "You help with the garden."},
                {"role": "user", "content": "when do tomatoes go out?"}]
    messages += recall_tool_rows(agent, "- tomatoes go out after the last frost", len(messages))
    kwargs = get_transport(mode).build_kwargs("m", messages, agent.tools, base_url="https://example.invalid/v1")
    defined, called = _names(mode, kwargs)
    assert called == {"memory_recall"}
    assert called <= defined
