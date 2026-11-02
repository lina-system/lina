"""hermes-agent is the agent's map of LINA: every ``lina`` command it names must be a command this
checkout builds, and every reference file must be routed from the skill."""

from __future__ import annotations

from tests.skills._skill_contract import assert_cli_commands_exist, assert_contract, assert_referenced_files_exist, load

SKILL = load("hermes-agent")


def test_document_contract():
    assert_contract(SKILL, complete_sections=True)


def test_names_real_lina_commands():
    assert len(assert_cli_commands_exist(SKILL)) >= 5


def test_every_reference_is_routed_and_exists():
    routed = set(assert_referenced_files_exist(SKILL))
    shipped = {p.relative_to(SKILL.directory).as_posix() for p in SKILL.directory.rglob("*")
               if p.is_file() and p.parent != SKILL.directory}
    assert shipped <= routed, sorted(shipped - routed)
