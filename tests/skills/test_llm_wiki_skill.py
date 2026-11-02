"""llm-wiki takes its location from ``skills.config.wiki.path``, not from ``.env``.

``.env`` holds secrets only (root AGENTS.md). The skill declares ``metadata.hermes.config`` so the
path is stored in config.yaml and arrives in the ``[Skill config ...]`` block when the skill loads.
"""

from __future__ import annotations

import os
from pathlib import Path

from agent.skill_commands import _inject_skill_config
from agent.skill_utils import extract_skill_config_vars, parse_frontmatter

REPO = Path(__file__).resolve().parents[2]
SKILL_MD = REPO / "skills" / "research" / "llm-wiki" / "SKILL.md"


def _loaded_block() -> str:
    parts: list[str] = []
    _inject_skill_config({"raw_content": SKILL_MD.read_text(encoding="utf-8")}, parts)
    return "\n".join(parts)


def test_declares_the_wiki_location_as_skill_config():
    frontmatter, _ = parse_frontmatter(SKILL_MD.read_text(encoding="utf-8"))
    declared = {var["key"]: var for var in extract_skill_config_vars(frontmatter)}
    assert declared["wiki.path"]["default"] == "~/wiki"


def test_configured_location_reaches_the_loaded_skill(tmp_path):
    wiki = tmp_path / "my-wiki"
    config = Path(os.environ["HERMES_HOME"]) / "config.yaml"
    config.write_text(f"skills:\n  config:\n    wiki:\n      path: {wiki}\n", encoding="utf-8")
    assert f"wiki.path = {wiki}" in _loaded_block()


def test_unset_location_falls_back_to_a_home_relative_default():
    config = Path(os.environ["HERMES_HOME"]) / "config.yaml"
    config.unlink(missing_ok=True)
    block = _loaded_block()
    assert "wiki.path = " in block and block.split("wiki.path = ", 1)[1].split("\n", 1)[0].endswith("wiki")


def test_body_no_longer_routes_the_location_through_env():
    body = SKILL_MD.read_text(encoding="utf-8")
    assert "WIKI_PATH" not in body
