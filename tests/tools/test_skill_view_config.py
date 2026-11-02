"""``skill_view`` returns a skill's resolved ``metadata.hermes.config`` settings.

A slash or preload load renders them as a ``[Skill config ...]`` block; a model that loads the skill
itself with ``skill_view(name=...)`` got no block and no values, so a skill such as llm-wiki could
not learn where its configured wiki lives. Exercised through the registry, as the model calls it.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
from unittest.mock import patch

import tools.skills_tool  # registers skill_view
from tools.registry import registry

SKILL = """---
name: notes-wiki
description: Keep notes.
metadata:
  hermes:
    config:
      - key: notes.path
        description: Where the notes live
        default: "~/notes"
---

# Notes
"""


def _view(skills_dir: Path, name: str) -> dict:
    with patch("tools.skills_tool.SKILLS_DIR", skills_dir):
        return json.loads(registry.dispatch("skill_view", {"name": name}))


def _skill(tmp_path: Path, text: str = SKILL, name: str = "notes-wiki") -> Path:
    skills_dir = tmp_path / "skills"
    (skills_dir / name).mkdir(parents=True)
    (skills_dir / name / "SKILL.md").write_text(text, encoding="utf-8")
    return skills_dir


def _config(text: str) -> None:
    (Path(os.environ["HERMES_HOME"]) / "config.yaml").write_text(text, encoding="utf-8")


def test_configured_value_is_returned(tmp_path):
    target = tmp_path / "my-notes"
    _config(f"skills:\n  config:\n    notes:\n      path: {target}\n")
    result = _view(_skill(tmp_path), "notes-wiki")
    assert result["success"] and result["skill_config"] == {"notes.path": str(target)}


def test_unset_value_falls_back_to_the_expanded_default(tmp_path):
    _config("skills: {}\n")
    value = _view(_skill(tmp_path), "notes-wiki")["skill_config"]["notes.path"]
    assert not value.startswith("~") and value.endswith("notes")


def test_skill_without_settings_has_no_config_field(tmp_path):
    plain = "---\nname: plain\ndescription: Plain.\n---\n\n# Plain\n"
    result = _view(_skill(tmp_path, plain, "plain"), "plain")
    assert result["success"] and "skill_config" not in result


def test_bundled_llm_wiki_reports_its_wiki_location(tmp_path):
    repo_skills = Path(__file__).resolve().parents[2] / "skills"
    _config(f"skills:\n  config:\n    wiki:\n      path: {tmp_path / 'kb'}\n")
    assert _view(repo_skills, "llm-wiki")["skill_config"] == {"wiki.path": str(tmp_path / "kb")}
