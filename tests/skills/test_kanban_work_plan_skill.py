"""Executable contracts of the kanban-work-plan bundled skill (lina#110).

The skill's example plan must be a plan `hermes kanban plan` accepts, every gate it names must be a
real `hermes kanban gate` subcommand with arguments that parse, and the frontmatter must carry what
the skills catalog reads.
"""

from __future__ import annotations

import argparse
import re
import shlex
from pathlib import Path

import pytest

import hermes_yaml
from agent.skill_utils import SKILL_PROMPT_DESC_LIMIT
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli import kanban_gates
from hermes_cli.kanban import kanban_command
from hermes_cli.kanban_parser import build_parser

SKILL_DIR = Path(__file__).resolve().parents[2] / "skills" / "autonomous-ai-agents" / "kanban-work-plan"
SKILL = (SKILL_DIR / "SKILL.md").read_text(encoding="utf-8")


def _example_plan() -> str:
    blocks = re.findall(r"```yaml\n(.*?)```", SKILL, flags=re.DOTALL)
    plans = [b for b in blocks if re.search(r"^cards:", b, flags=re.MULTILINE)]
    assert len(plans) == 1, "SKILL.md must carry exactly one example plan"
    return plans[0]


def _kanban(capsys, *argv) -> tuple[int, str, str]:
    parser = argparse.ArgumentParser()
    build_parser(parser.add_subparsers())
    try:
        rc = kanban_command(parser.parse_args(["kanban", *argv]))
    except SystemExit as exc:
        rc = exc.code
    out = capsys.readouterr()
    return rc, out.out, out.err


@pytest.fixture
def board(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    plan = hermes_yaml.safe_load(_example_plan())
    for profile in {plan["goal"]["assignee"], *(c["assignee"] for c in plan["cards"])}:
        (home / "profiles" / profile).mkdir(parents=True)
        (home / "profiles" / profile / "config.yaml").write_text("{}\n", encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    return tmp_path


def test_example_plan_passes_dry_run(board, capsys):
    path = board / "plan.yaml"
    path.write_text(_example_plan(), encoding="utf-8")
    rc, out, err = _kanban(capsys, "plan", str(path), "--dry-run")
    assert rc == 0, err
    assert "dry run, nothing written" in out
    with kbc.connect_closing() as conn:
        assert kb.list_tasks(conn) == []


def _gate_commands() -> tuple[list[list[str]], set[str]]:
    plan = hermes_yaml.safe_load(_example_plan())
    commands = [shlex.split(g) for card in plan["cards"] for g in card["gates"]]
    named = set(re.findall(r"(?:`|hermes kanban )gate ([a-z]+)\b", SKILL))
    return commands, named


def test_every_named_gate_exists(capsys):
    rc, out, _err = _kanban(capsys, "gate", "--help")
    assert rc == 0
    listed = set(re.search(r"\b([a-z]+(?:\|[a-z]+)+)\b", out).group(1).split("|"))
    commands, named = _gate_commands()
    assert named and named <= listed, f"unknown gates: {sorted(named - listed)}"
    for argv in commands:
        assert argv[:3] == ["hermes", "kanban", "gate"], argv
        kanban_gates.build_parser().parse_args(argv[3:])


def test_frontmatter_has_catalog_fields():
    match = re.match(r"---\n(.*?)\n---\n", SKILL, flags=re.DOTALL)
    assert match, "SKILL.md must start with a frontmatter block"
    fm = hermes_yaml.safe_load(match.group(1))
    for field in ("name", "description", "version", "author", "license", "platforms"):
        assert fm.get(field), field
    assert fm["name"] == SKILL_DIR.name
    assert len(fm["description"]) <= SKILL_PROMPT_DESC_LIMIT and fm["description"].endswith(".")
    hermes = fm["metadata"]["hermes"]
    assert hermes["tags"]
    assert hermes["category"] == SKILL_DIR.parent.name
