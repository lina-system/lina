"""lina#21: with the owner's threshold, the bundled LCM-X engine starts compaction at 700k tokens of a 1M window.

The home says ``lcm.context_threshold: 0.70`` (an inherited ``compression.threshold`` below LCM-X's 0.75 default
is raised to it; ``lcm.context_threshold`` is not). Token counts come from LCM-X's own estimator, the one its
preflight gate uses; nothing reaches the network.
"""

from __future__ import annotations

import sys
from unittest.mock import patch

import pytest

WINDOW, TRIGGER = 1_000_000, 700_000


@pytest.fixture
def engine(tmp_path, monkeypatch):
    from hermes_cli.plugins import _reset_plugin_managers_for_tests

    monkeypatch.setenv("LCM_EMBEDDINGS_ENABLED", "false")
    monkeypatch.setitem(sys.modules, "tiktoken", None)
    home = tmp_path / ".hermes"
    home.mkdir()
    (home / "config.yaml").write_text(
        "context:\n  engine: lcm-x\n"
        f"model:\n  default: test/model\n  context_length: {WINDOW}\n"
        "lcm:\n  context_threshold: 0.70\n", encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(home))
    _reset_plugin_managers_for_tests()
    from run_agent import AIAgent

    with (
        patch("model_tools.get_tool_definitions", return_value=[]),
        patch("model_tools.check_toolset_requirements", return_value={}),
        patch("agent.process_bootstrap.OpenAI"),
    ):
        agent = AIAgent(api_key="test-key-1234567890", base_url="https://openrouter.ai/api/v1", model="test/model",
                        quiet_mode=True, skip_context_files=True, skip_memory=True, session_id="sess-trigger")
    yield agent.context_compressor
    _reset_plugin_managers_for_tests()


def _messages_of(tokens: int) -> list[dict]:
    """A transcript LCM-X's estimator counts at exactly ``tokens``."""
    from importlib import import_module

    count = import_module("plugins.context_engine.lcm-x.tokens").count_messages_tokens
    turns = [{"role": "user", "content": "x"}, {"role": "assistant", "content": "y"}]
    low, high = 0, tokens * 8
    while low < high:
        mid = (low + high) // 2
        if count([*turns, {"role": "user", "content": "z" * mid}]) < tokens:
            low = mid + 1
        else:
            high = mid
    messages = [*turns, {"role": "user", "content": "z" * low}]
    assert count(messages) == tokens
    return messages


def test_owner_threshold_compacts_at_700k_of_a_1m_window_and_not_before(engine):
    assert type(engine).__module__ == "plugins.context_engine.lcm-x.engine"
    assert (engine.context_length, engine.threshold_tokens) == (WINDOW, TRIGGER)

    assert not engine.should_compress(TRIGGER - 1)
    assert engine.should_compress(TRIGGER)
    assert not engine.should_compress_preflight(_messages_of(TRIGGER - 1))
    assert engine.should_compress_preflight(_messages_of(TRIGGER))
