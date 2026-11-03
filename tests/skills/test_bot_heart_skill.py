"""bot-heart keeps a journal and a guarded self note: on LINA through `lina heart` (every command it names must be
a parser this checkout builds), elsewhere through file tools that exist. Its daily review template ships with it,
and it is offered only where `lina heart` loads (POSIX: the work record takes an fcntl lock)."""

from __future__ import annotations

from tests.skills._skill_contract import assert_cli_commands_exist, assert_contract, assert_tools_registered, load

SKILL = load("bot-heart")


def test_document_contract():
    assert_contract(SKILL, complete_sections=True)


def test_names_only_lina_heart_commands_that_exist_and_registered_file_tools():
    assert_tools_registered(SKILL, ("read_file", "write_file", "patch"))
    named = assert_cli_commands_exist(SKILL)
    assert named and all(phrase.startswith("heart") for phrase in named)


def test_the_review_template_ships_beside_it_and_names_existing_commands():
    template = SKILL.directory / "templates" / "heart-review.md"
    assert template.is_file()
    assert "lina heart candidates" in template.read_text(encoding="utf-8")


def test_offered_only_on_platforms_where_lina_heart_loads():
    assert SKILL.frontmatter["platforms"] == ["linux", "macos"]
