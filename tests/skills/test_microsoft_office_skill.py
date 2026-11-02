"""microsoft-office: every helper script the Office procedure names ships and compiles, so the model never runs
a path that is missing or broken."""

from __future__ import annotations

from tests.skills._skill_contract import assert_contract, assert_referenced_files_exist, load

SKILL = load("microsoft-office")


def test_document_contract():
    assert_contract(SKILL, complete_sections=True)


def test_named_scripts_ship_and_compile():
    import py_compile

    scripts = [ref for ref in assert_referenced_files_exist(SKILL) if ref.startswith("scripts/")]
    assert scripts
    for ref in scripts:
        py_compile.compile(str(SKILL.directory / ref), doraise=True)
    assert "docker" not in SKILL.body.lower()
