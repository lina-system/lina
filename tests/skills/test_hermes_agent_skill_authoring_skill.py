"""hermes-agent-skill-authoring is the guide the agent follows to write a bundled skill: the limits
and validator it cites must be the ones this checkout enforces, and it must not send in-repo edits
through ``skill_manage``."""

from __future__ import annotations

from tests.skills._skill_contract import assert_contract, assert_tools_registered, load

SKILL = load("hermes-agent-skill-authoring")


def test_document_contract():
    assert_contract(SKILL, complete_sections=False)


def test_cites_the_enforced_limit_and_validator():
    import tools.skill_manager_tool as manager
    from agent.skill_utils import SKILL_PROMPT_DESC_LIMIT

    assert f"{SKILL_PROMPT_DESC_LIMIT} chars" in SKILL.body or f"over {SKILL_PROMPT_DESC_LIMIT}" in SKILL.body
    assert "tools/skill_manager_tool.py::_validate_frontmatter" in SKILL.body
    assert callable(manager._validate_frontmatter)


def test_in_repo_edits_use_file_tools():
    editing = SKILL.body.split("## Editing Existing In-Repo Skills", 1)[1].split("\n## ", 1)[0]
    assert "skill_manage(" not in editing and "`patch`" in editing and "`write_file`" in editing
    assert_tools_registered(SKILL, ("patch", "write_file", "skill_manage", "search_files"))
