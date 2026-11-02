"""bot-heart is a file-based journal procedure: it may only call file tools that exist, and it
names no CLI command."""

from __future__ import annotations

from tests.skills._skill_contract import assert_cli_commands_exist, assert_contract, assert_tools_registered, load

SKILL = load("bot-heart")


def test_document_contract():
    assert_contract(SKILL, complete_sections=True)


def test_uses_only_registered_file_tools_and_no_cli():
    assert_tools_registered(SKILL, ("read_file", "write_file", "patch"))
    assert assert_cli_commands_exist(SKILL) == []
