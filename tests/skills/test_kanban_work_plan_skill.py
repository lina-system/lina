"""Executable contracts of the kanban-work-plan bundled skill (lina#110).

The skill's example plan must be a plan `hermes kanban plan` accepts, every gate it names must be a
real `hermes kanban gate` subcommand with arguments that parse, and the frontmatter must carry what
the skills catalog reads.
"""

from __future__ import annotations

import argparse
import json
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
    _example_record(tmp_path, home, plan)
    return tmp_path


def _example_record(tmp_path: Path, home: Path, plan: dict) -> None:
    """A sqlite work record holding the example's ledger goal and the to-dos it names (lina#163 reads them)."""
    from hermes_cli.work import env, main
    goal = plan["goal"]["ledger_goal"]
    (home / "config.yaml").write_text("work:\n  store: {backend: sqlite}\n", encoding="utf-8")
    base = {"by": plan["goal"]["assignee"], "state": "open", "created": 1800000000, "progress_at": 1800000000,
            "op": "add", "writer": plan["goal"]["assignee"], "links": [], "notes": []}
    rows = [{**base, "id": "A1", "kind": "영역", "title": "Dev", "parent": ""},
            {**base, "id": goal, "kind": "목표", "title": "Example goal", "parent": "A1", "check": "false",
             "done": "Example goal is done", "basis": [{"text": "An example", "at": 1800000000, "src": "test"}]}]
    for card in plan["cards"]:
        for todo in card.get("ledger_todos") or []:
            if isinstance(todo, str):
                rows.append({**base, "id": todo, "kind": "할 일", "title": f"Example {todo}", "parent": goal,
                             "by": card["assignee"], "session": f"{card['assignee']}:example"})
    src = tmp_path / "ledger.jsonl"
    src.write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in rows), encoding="utf-8")
    env.reset()
    try:
        main(["import", "--from", str(src)])
    finally:
        env.reset()


def test_example_plan_passes_dry_run(board, capsys):
    path = board / "plan.yaml"
    path.write_text(_example_plan(), encoding="utf-8")
    rc, out, err = _kanban(capsys, "plan", str(path), "--dry-run")
    assert rc == 0, err
    assert "dry run, nothing written" in out
    assert "record goal: " in out and "to-do: link " in out and "to-do: create " in out
    with kbc.connect_closing() as conn:
        assert kb.list_tasks(conn) == []


def _gate_commands() -> tuple[list[list[str]], set[str]]:
    plan = hermes_yaml.safe_load(_example_plan())
    commands = [shlex.split(g) for card in plan["cards"] for g in card["gates"]]
    named = set(re.findall(r"(?:`|hermes kanban )gate ([a-z]+(?:-[a-z]+)*)", SKILL))
    return commands, named


def test_example_metadata_survives_plan_body_rendering():
    from hermes_cli.kanban_plan import parse_plan, work_card_body

    # Given the executable example with optional work-card metadata.
    data = hermes_yaml.safe_load(_example_plan())

    # When the actual plan parser and body renderer consume it.
    plan = parse_plan(data)
    body = work_card_body(plan.cards[0], "K1", plan.goal.title)

    # Then the work body preserves the machine-consumed field values.
    fields = dict(line.split(": ", 1) for line in body.splitlines() if ": " in line)
    assert fields["Size"] == data["cards"][0]["size"]
    assert fields["Kind"] == data["cards"][0]["kind"]


def test_every_named_gate_exists(capsys):
    rc, out, _err = _kanban(capsys, "gate", "--help")
    assert rc == 0
    listed = set(re.search(r"\b([a-z-]+(?:\|[a-z-]+)+)\b", out).group(1).split("|"))
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
