"""long-task-sessions tells the model how to launch and track long work; every tool and argument it
names must exist, or the model calls a parameter the tool rejects."""

from __future__ import annotations

from tests.skills._skill_contract import assert_contract, assert_tools_registered, load

SKILL = load("long-task-sessions")


def test_document_contract():
    assert_contract(SKILL, complete_sections=True)


def test_named_tools_and_arguments_exist():
    assert_tools_registered(SKILL, ("terminal", "delegate_task", "write_file", "patch", "skill_view"))
    assert "terminal(background=" in SKILL.body
