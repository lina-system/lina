"""xurl keeps its secret-safety rules ahead of every command section and assumes no Docker HOME."""

from __future__ import annotations

from tests.skills._skill_contract import assert_contract, load, sections

SKILL = load("xurl")


def test_document_contract():
    assert_contract(SKILL, complete_sections=False)


def test_secret_safety_comes_first():
    titles = sections(SKILL.body)
    assert titles[0].startswith("Secret Safety"), titles
    assert "docker" not in SKILL.body.lower()
