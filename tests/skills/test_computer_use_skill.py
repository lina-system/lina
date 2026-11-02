"""computer-use routes macOS permissions through LINA Helper; the helper reference and the code it
points at must exist, and the ``computer_use`` tool must be registered."""

from __future__ import annotations

from tests.skills._skill_contract import REPO, assert_contract, assert_referenced_files_exist, assert_tools_registered, load

SKILL = load("computer-use")


def test_document_contract():
    assert_contract(SKILL, complete_sections=False)


def test_lina_helper_route_exists():
    assert "references/lina-helper.md" in assert_referenced_files_exist(SKILL)
    helper = (SKILL.directory / "references" / "lina-helper.md").read_text(encoding="utf-8")
    for path in ("helper/", "hermes_cli/helper_install.py"):
        assert path in helper and (REPO / path).exists(), path
    assert_tools_registered(SKILL, ("computer_use", "browser_exec", "read_file"))
