"""onepassword-agents must hand secrets to the consumer through ``op run`` and never show a command
that prints one."""

from __future__ import annotations

from tests.skills._skill_contract import assert_contract, assert_tools_registered, load

SKILL = load("onepassword-agents")


def test_document_contract():
    assert_contract(SKILL, complete_sections=True)


def test_secrets_flow_through_op_run_only():
    commands = [line.strip() for line in SKILL.body.splitlines() if line.strip().startswith(("op ", "API_TOKEN="))]
    assert any(" op run -- " in f" {c}" for c in commands), commands
    for leak in ("op read", "--reveal", "set -x", "echo $", "printenv"):
        assert leak not in SKILL.body, leak
    assert_tools_registered(SKILL, ("terminal",))
