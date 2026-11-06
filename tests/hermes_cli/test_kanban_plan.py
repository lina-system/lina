"""``hermes kanban plan <file>`` on a scratch board (lina#110).

One plan lays out a goal card and its gated work cards in one transaction: work cards are
link-parents of the goal card, ``after:`` becomes a link, a re-run returns the same ids, and a
refused plan writes nothing.
"""

from __future__ import annotations

import argparse
import copy
import json
from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.kanban import kanban_command
from hermes_cli.kanban_parser import build_parser

PLAN = {
    "key": "lina-m0-work-sessions",
    "goal": {"title": "Run a goal through work sessions", "assignee": "sion",
             "body": "Owner goal for M0 S3."},
    "cards": [
        {"key": "gates", "title": "Card goal gates", "assignee": "sion",
         "todos": ["lina-system/lina#108", "[ ] write the gate doc"],
         "outcome": "Gates run before the judge.", "verification": "Gate tests green.",
         "constraints": "No new env vars.", "boundaries": "Kanban only.",
         "stop_when": "PR merged and dev green.", "worker": "omo — multi-file change",
         "gates": ["hermes kanban gate prs", "hermes kanban gate dev lina-system/lina"],
         "max_turns": 40, "max_runtime": "12h"},
        {"key": "plan-cmd", "title": "Plan command", "assignee": "sion",
         "todos": ["lina-system/lina#110"], "outcome": "Plans lay out cards.",
         "verification": "Plan tests green.", "stop_when": "PR merged.", "worker": "omo",
         "gates": ["hermes kanban gate prs"], "max_turns": 30, "max_runtime": "6h",
         "after": ["gates"]},
        {"key": "review", "title": "Review the plan command", "assignee": "rev",
         "todos": ["[ ] review the plan command PR"], "outcome": "An approval by rev.",
         "verification": "Review recorded.", "stop_when": "Approved.", "worker": "rev — reviewer",
         "gates": ["hermes kanban gate review"], "max_turns": 10, "max_runtime": "2h",
         "after": ["plan-cmd"]},
    ],
}


@pytest.fixture
def board(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    for profile in ("sion", "rev"):
        (home / "profiles" / profile).mkdir(parents=True)
        (home / "profiles" / profile / "config.yaml").write_text("{}\n", encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("HERMES_PROFILE", "sion")
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    return tmp_path


def _write(tmp_path, plan, name="plan.yaml") -> str:
    path = tmp_path / name
    if name.endswith(".json"):
        path.write_text(json.dumps(plan), encoding="utf-8")
    else:
        from hermes_yaml import safe_dump
        # A BOM, as editors on Windows write it: the loader must read it as utf-8-sig.
        path.write_text("\ufeff" + safe_dump(plan), encoding="utf-8")
    return str(path)


def _run(capsys, *argv) -> tuple[int, str, str]:
    parser = argparse.ArgumentParser()
    build_parser(parser.add_subparsers())
    rc = kanban_command(parser.parse_args(["kanban", "plan", *argv]))
    out = capsys.readouterr()
    return rc, out.out, out.err


def _plan(capsys, path) -> dict:
    rc, out, err = _run(capsys, path, "--json")
    assert rc == 0, err
    return json.loads(out)


def _tasks():
    with kbc.connect_closing() as conn:
        return {t.id: t for t in kb.list_tasks(conn)}


def _links():
    with kbc.connect_closing() as conn:
        return {(r["parent_id"], r["child_id"])
                for r in conn.execute("SELECT parent_id, child_id FROM task_links")}


def _finish(task_id):
    with kbc.connect_closing() as conn:
        assert kb.complete_task(conn, task_id, result="done", force=True)
        kb.recompute_ready(conn)


def test_goal_card_waits_for_every_work_card(board, capsys):
    desc = _plan(capsys, _write(board, PLAN))
    ids = {c["key"]: c["id"] for c in desc["cards"]}
    goal_id = desc["goal"]["id"]

    links = _links()
    assert {(ids[k], goal_id) for k in ids} <= links
    assert (ids["gates"], ids["plan-cmd"]) in links and (ids["plan-cmd"], ids["review"]) in links
    assert len(links) == 5

    tasks = _tasks()
    assert tasks[goal_id].status == "todo" and not tasks[goal_id].goal_mode
    assert tasks[ids["gates"]].status == "ready"
    assert tasks[ids["plan-cmd"]].status == "todo" and tasks[ids["review"]].status == "todo"

    card = tasks[ids["gates"]]
    assert card.goal_mode and card.assignee == "sion" and card.goal_max_turns == 40
    assert card.max_runtime_seconds == 12 * 3600
    assert [g["command"] for g in json.loads(card.goal_gates)] == PLAN["cards"][0]["gates"]
    headings = [line for line in card.body.splitlines() if line.startswith("## ")]
    assert headings == ["## Goal", "## To-dos", "## Outcome", "## Verification", "## Constraints",
                        "## Boundaries", "## Stop when", "## Worker", "## Record"]
    assert f"{goal_id} — {PLAN['goal']['title']}" in card.body
    assert "- lina-system/lina#108\n- [ ] write the gate doc" in card.body
    assert tasks[ids["review"]].assignee == "rev"

    _finish(ids["gates"])
    assert _tasks()[ids["plan-cmd"]].status == "ready"
    _finish(ids["plan-cmd"])
    assert _tasks()[goal_id].status == "todo"
    _finish(ids["review"])
    assert _tasks()[goal_id].status == "ready"


def test_rerun_returns_the_same_ids(board, capsys):
    path = _write(board, PLAN, "plan.json")
    first = _plan(capsys, path)
    again = _plan(capsys, path)
    assert first["created"] and not again["created"]
    assert again["goal"]["id"] == first["goal"]["id"]
    assert [c["id"] for c in again["cards"]] == [c["id"] for c in first["cards"]]
    assert len(_tasks()) == 4 and len(_links()) == 5


@pytest.mark.parametrize("size", ["S", "M", "L"])
def test_work_card_metadata_reaches_body_when_supplied(board, capsys, size):
    # Given optional planning metadata on a work card.
    plan = copy.deepcopy(PLAN)
    plan["cards"][0].update(size=size, kind="implementation")

    # When the real plan command writes the board.
    desc = _plan(capsys, _write(board, plan))

    # Then the stored body retains the metadata as labeled fields.
    body = _tasks()[desc["cards"][0]["id"]].body
    fields = dict(line.split(": ", 1) for line in body.splitlines() if ": " in line)
    assert fields["Size"] == size
    assert fields["Kind"] == "implementation"


@pytest.mark.parametrize("size", ["XL", "small", 3, True, ["S"]])
def test_invalid_size_is_refused_before_board_write(board, capsys, size):
    # Given a size outside the plan's three supported sizes.
    plan = copy.deepcopy(PLAN)
    plan["cards"][0]["size"] = size

    # When the real plan command validates it.
    rc, _out, err = _run(capsys, _write(board, plan))

    # Then it names the size contract and leaves the board untouched.
    assert rc != 0 and "size must be S, M or L" in err
    assert _tasks() == {} and _links() == set()


def _refused(**change):
    plan = copy.deepcopy(PLAN)
    for path, value in change.items():
        target = plan
        *parents, last = path.split("__")
        for part in parents:
            target = target[int(part)] if part.isdigit() else target[part]
        target[last] = value
    return plan


@pytest.mark.parametrize("plan, names", [
    (_refused(cards__1__gates=[]), "card 'plan-cmd' has no gate"),
    (_refused(cards__2__todos=["lina-system/lina#108"]), "card 'review': to-do 'lina-system/lina#108' is already in card 'gates'"),
    (_refused(cards__2__assignee="nobody"), "card 'review': assignee 'nobody' is not a Hermes profile"),
    (_refused(goal__assignee="nobody"), "goal: assignee 'nobody' is not a Hermes profile"),
    (_refused(cards__2__after=["missing"]), "card 'review': after names unknown card 'missing'"),
    (_refused(cards__0__after=["review"]), "after makes a cycle"),
    (_refused(cards__1__triage=True), "card 'plan-cmd' is marked for triage/decomposition"),
    (_refused(decompose=True), "plan is marked for triage/decomposition"),
])
def test_refusal_names_the_card_and_writes_nothing(board, capsys, plan, names):
    rc, _out, err = _run(capsys, _write(board, plan))
    assert rc != 0
    assert "refused, nothing written" in err and names in err
    assert _tasks() == {} and _links() == set()


@pytest.mark.parametrize("kind, gates, refused", [
    ("screen", ["hermes kanban gate prs"], True),
    ("Screen", ["hermes kanban gate prs", "hermes kanban gate ask"], True),
    ("screen", ["hermes kanban gate artifact ui-qa.md --check true"], True),
    ("screen", ["hermes kanban gate prs", "hermes kanban gate ui-qa"], False),
    ("screen", [{"command": "hermes kanban gate ui-qa --author qa"}], False),
    ("screen", ["python3 kanban_gates.py --db board.db ui-qa"], False),
    ("screen", ["python3 hermes_cli/kanban_gates.py ui-qa"], False),
    ("screen", ["hermes kanban --board work gate ui-qa --card X"], False),
    ("screen", ["/opt/bin/lina -p qa kanban gate ui-qa"], False),
    ("screen", ["echo gate ui-qa"], True),
    ("screen", ["hermes kanban gate review --owner ui-qa"], True),
    ("screen", [{"command": "echo gate ui-qa"}], True),
    ("screen", ["sh -c 'hermes kanban gate ui-qa'"], True),
    ("screen", ["env FOO=1 hermes kanban gate ui-qa"], True),
    ("screen", ["hermes kanban gate ui-qa || true"], True),
    ("screen", ["hermes kanban show ui-qa"], True),
    ("implementation", ["hermes kanban gate prs"], False),
])
def test_screen_card_needs_the_ui_qa_gate(board, capsys, kind, gates, refused):
    """A card marked ``kind: screen`` changes what a person sees, so it lists ``gate ui-qa`` (lina#276)."""
    plan = _refused(cards__1__kind=kind, cards__1__gates=gates)
    rc, _out, err = _run(capsys, _write(board, plan))
    if refused:
        assert rc != 0 and "refused, nothing written" in err
        assert "card 'plan-cmd': kind screen changes a screen, so its gates need `hermes kanban gate ui-qa`" in err
        assert _tasks() == {} and _links() == set()
    else:
        assert rc == 0, err
        assert len(_tasks()) == 4


def test_delegate_child_cannot_lay_out_a_plan(board, capsys):
    from agent.delegation_context import delegated_child_context

    with delegated_child_context():
        rc, _out, err = _run(capsys, _write(board, PLAN))
    assert rc != 0
    assert "delegate_task child contexts cannot mutate Kanban tasks via the CLI" in err
    assert _tasks() == {} and _links() == set()


def test_dry_run_writes_nothing(board, capsys):
    rc, out, err = _run(capsys, _write(board, PLAN), "--dry-run")
    assert rc == 0, err
    assert "dry run, nothing written" in out
    assert "gate: hermes kanban gate dev lina-system/lina" in out
    assert "link  gates -> plan-cmd" in out and "link  review -> goal" in out
    assert _tasks() == {} and _links() == set()


def test_reserved_goal_key_rejected_during_validation(board):
    from hermes_cli.kanban_plan import PlanError, parse_plan

    plan = copy.deepcopy(PLAN)
    plan["cards"] = [plan["cards"][0] | {"key": "goal"}]
    with pytest.raises(PlanError, match="goal.*reserved"):
        parse_plan(plan)
    assert _tasks() == {} and _links() == set()


@pytest.mark.parametrize("dry_run", [False, True])
def test_reserved_goal_key_cli_writes_nothing(board, capsys, dry_run):
    plan = copy.deepcopy(PLAN)
    plan["cards"] = [plan["cards"][0] | {"key": "goal"}]
    rc, _out, err = _run(capsys, _write(board, plan), *(["--dry-run"] if dry_run else []))
    assert rc != 0 and "goal" in err and "reserved" in err
    assert _tasks() == {} and _links() == set()


@pytest.mark.parametrize("change", ["remove", "add"])
def test_plan_membership_changes_are_refused(board, capsys, change):
    _plan(capsys, _write(board, PLAN))
    tasks, links = _tasks(), _links()
    changed = copy.deepcopy(PLAN)
    if change == "remove":
        changed["cards"].pop()
    else:
        changed["cards"].append(changed["cards"][-1] | {"key": "extra", "todos": ["[ ] extra work"]})

    rc, _out, err = _run(capsys, _write(board, changed))

    assert rc != 0 and "a laid-out plan cannot gain or lose cards" in err
    assert _tasks() == tasks and _links() == links


@pytest.mark.parametrize("key,other", [("a", "ab"), ("a_b", "axb")])
def test_plan_membership_prefix_is_exact_and_rerun_is_unchanged(board, capsys, key, other):
    plan = copy.deepcopy(PLAN)
    plan["key"] = other
    _plan(capsys, _write(board, plan))
    plan["key"] = key
    first = _plan(capsys, _write(board, plan))

    again = _plan(capsys, _write(board, plan))

    assert first["created"] and not again["created"]
    assert again["goal"]["id"] == first["goal"]["id"]
    assert [c["id"] for c in again["cards"]] == [c["id"] for c in first["cards"]]
    assert len(_tasks()) == 8 and len(_links()) == 10
