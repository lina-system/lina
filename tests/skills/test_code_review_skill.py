"""code-review is the skill the kanban review lane and the swarm verifier preload by name; its
verdict section must map each outcome to a kanban tool that exists."""

from __future__ import annotations

from unittest.mock import patch

from tests.skills._skill_contract import REPO, assert_contract, assert_tools_registered, load

SKILL = load("code-review")


def test_document_contract():
    assert_contract(SKILL, complete_sections=True)


def test_verdicts_map_to_registered_kanban_tools():
    lane = SKILL.body.split("## Kanban review lane", 1)[1].split("\n## ", 1)[0]
    assert "`kanban_show`" in lane.split("verdict", 1)[0]
    verdicts = {
        "approve": ("kanban_complete",),
        "request changes": ("kanban_comment", "kanban_request_changes"),
        "escalate": ("kanban_block",),
    }
    for verdict, tools in verdicts.items():
        clause = lane.split(verdict, 1)[1].split(";", 1)[0]
        assert all(f"`{tool}`" in clause for tool in tools), (verdict, clause)
    assert_tools_registered(SKILL, ("kanban_show", "kanban_complete", "kanban_comment",
                                    "kanban_request_changes", "kanban_block"))
    assert "Never edit the implementation" in lane


def test_swarm_verifier_preloads_this_skill(tmp_path):
    from agent.skill_commands import build_preloaded_skills_prompt
    from hermes_cli import kanban_db as kb
    from hermes_cli import kanban_db_connect as kbc
    from hermes_cli.kanban_swarm import SwarmWorkerSpec, create_swarm

    conn = kbc.connect(tmp_path / "kanban.db")
    try:
        created = create_swarm(conn, goal="Ship one change.",
                               workers=[SwarmWorkerSpec(profile="dev", title="Build", body="Build it")],
                               verifier_assignee="reviewer", synthesizer_assignee="writer",
                               tenant="t", created_by="lead")
        skills = list(kb.get_task(conn, created.verifier_id).skills or ())
    finally:
        conn.close()
    with patch("tools.skills_tool.SKILLS_DIR", REPO / "skills"):
        prompt, loaded, missing = build_preloaded_skills_prompt(skills)
    assert skills == ["code-review"] and loaded == ["code-review"] and not missing
    assert "## Kanban review lane" in prompt
