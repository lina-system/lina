"""claude-design points only at skills that ship; it gives no install command for a skill this
checkout does not carry."""

from __future__ import annotations

from tests.skills._skill_contract import REPO, assert_contract, load

SKILL = load("claude-design")


def test_document_contract():
    assert_contract(SKILL, complete_sections=False)


def test_related_skills_ship_and_no_install_hints():
    shipped = {p.parent.name for p in (REPO / "skills").glob("*/*/SKILL.md")}
    related = (SKILL.frontmatter.get("metadata") or {}).get("hermes", {}).get("related_skills") or []
    assert set(related) <= shipped, related
    assert "skills install" not in SKILL.body
