"""lina#380: a Korean agent's Korean writing rule rides the frozen plugin-section slot; other agents carry none."""
from __future__ import annotations

import pytest

from agent.korean_rule_section import KOREAN_RULE
from agent.system_prompt import build_system_prompt, invalidate_system_prompt
from hermes_cli import plugins
from hermes_cli.plugins import PLUGIN_SECTIONS_END, PluginManager
from hermes_constants import reset_hermes_home_override, set_hermes_home_override
from run_agent import AIAgent

FRAME = "## Plugin Context: agent-korean-writing"


def _home(tmp_path, monkeypatch, language: str):
    from agent.i18n import reset_language_cache
    monkeypatch.setattr("agent.coding_context.build_coding_workspace_block",
                        lambda cwd=None: "Workspace (snapshot at session start):\n- Root: /pinned")
    manager = PluginManager()
    manager._discovered = True
    monkeypatch.setattr(plugins, "_plugin_manager", manager)
    path = tmp_path / "home"
    path.mkdir()
    (path / "config.yaml").write_text(f"display:\n  language: {language}\n", encoding="utf-8")
    token = set_hermes_home_override(path)
    reset_language_cache()
    return token


@pytest.fixture
def korean(tmp_path, monkeypatch):
    token = _home(tmp_path, monkeypatch, "ko")
    yield
    reset_hermes_home_override(token)


@pytest.fixture
def english(tmp_path, monkeypatch):
    token = _home(tmp_path, monkeypatch, "en")
    yield
    reset_hermes_home_override(token)


def _agent(session_id: str) -> AIAgent:
    return AIAgent(api_key="test-key", base_url="https://openrouter.ai/api/v1", model="test/model",
                   provider="openrouter", platform="cli", quiet_mode=True, skip_context_files=True,
                   skip_memory=True, session_id=session_id)


def _section(prompt: str) -> str:
    return prompt[prompt.index(FRAME):prompt.index(PLUGIN_SECTIONS_END)]


def test_a_korean_agent_carries_the_rule_with_the_same_bytes_in_every_build(korean):
    agent = _agent("20261116_090000_aaaaaa")
    first = build_system_prompt(agent)
    invalidate_system_prompt(agent)
    rebuilt = build_system_prompt(agent)
    other_session = build_system_prompt(_agent("20261116_180000_bbbbbb"))

    section = _section(first)
    assert KOREAN_RULE in section
    for topic in ("머리말", "상대의 질문", "사과", "맺음말", "실제 상태", "약어와 경로"):
        assert any(line.startswith("- ") and topic in line for line in section.splitlines()), topic
    assert first.index(FRAME) < first.index("Conversation started:")
    assert _section(rebuilt) == _section(other_session) == section


def test_an_agent_whose_language_is_not_korean_carries_no_rule(english):
    prompt = build_system_prompt(_agent("20261116_090000_cccccc"))

    assert FRAME not in prompt and KOREAN_RULE not in prompt
