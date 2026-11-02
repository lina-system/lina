"""claude-code describes Claude Code as a runner LINA dispatches work to through ``terminal``; the tool and its
arguments must exist and no Docker build recipe may come back."""

from __future__ import annotations

from tests.skills._skill_contract import assert_contract, assert_tools_registered, load

SKILL = load("claude-code")


def test_document_contract():
    assert_contract(SKILL, complete_sections=True)


def test_runs_through_registered_terminal_tool():
    assert_tools_registered(SKILL, ("terminal",))
    assert "docker build" not in SKILL.body.lower()
