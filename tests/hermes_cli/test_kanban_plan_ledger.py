"""``hermes kanban plan`` writes a plan's goal and to-dos into the work record (lina#163).

A scratch board and a scratch work root (sqlite store) share one HERMES_HOME. The plan runs as profile alpha; card
assignees alpha and beta are both profiles and record agents, so only beta is told about its card.
"""

from __future__ import annotations

import argparse
import copy
import json
import sys

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.kanban import kanban_command
from hermes_cli.kanban_parser import build_parser
from tests.hermes_cli._work_env import WorkEnv

NEW_OWN = {"title": "기기 2 점검", "why": "작업이 멈추지 않게", "done": "기기가 답한다", "check": "false"}
NEW_OTHER = {"title": "기기 3 점검", "why": "작업이 멈추지 않게", "done": "기기가 답한다", "check": "false"}


def _card(key: str, assignee: str, ledger_todos: list) -> dict:
    return {"key": key, "title": f"Card {key}", "assignee": assignee, "todos": [f"[ ] {key} work"],
            "ledger_todos": ledger_todos, "outcome": "Done.", "verification": "Checked.",
            "stop_when": "Done.", "worker": "omo", "gates": ["hermes kanban gate prs"],
            "max_turns": 5, "max_runtime": "1h"}


PLAN = {
    "key": "record-plan",
    "goal": {"title": "Machines tidy", "assignee": "alpha", "ledger_goal": "G1"},
    "cards": [_card("own", "alpha", [NEW_OWN]), _card("other", "beta", ["T1", NEW_OTHER])],
}


@pytest.fixture
def work(tmp_path, monkeypatch):
    work = WorkEnv(tmp_path, monkeypatch)
    for profile in ("alpha", "beta"):
        (work.root / "profiles" / profile).mkdir(parents=True)
        (work.root / "profiles" / profile / "config.yaml").write_text("{}\n", encoding="utf-8")
    monkeypatch.setenv("HERMES_PROFILE", "alpha")
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    work.goal_and_todos()
    return work


def _run(work, capsys, plan, *argv) -> tuple[int, str, str]:
    path = work.tmp / "plan.json"
    path.write_text(json.dumps(plan), encoding="utf-8")
    parser = argparse.ArgumentParser()
    build_parser(parser.add_subparsers())
    capsys.readouterr()
    rc = kanban_command(parser.parse_args(["kanban", "plan", str(path), *argv]))
    out = capsys.readouterr()
    return rc, out.out, out.err


def _plan(work, capsys, plan=PLAN) -> dict:
    rc, out, err = _run(work, capsys, plan, "--json")
    assert rc == 0, err
    return json.loads(out)


def _record() -> dict:
    from hermes_cli.work import env, ledger
    env.reset()
    try:
        recs, _ = ledger.read_all()
        return {i: dict(r) for i, r in recs.items()}
    finally:
        ledger.close()


def _tasks() -> dict:
    with kbc.connect_closing() as conn:
        return {t.id: t for t in kb.list_tasks(conn)}


def _comments(task_id: str) -> list[str]:
    with kbc.connect_closing() as conn:
        return [c.body for c in kb.list_comments(conn, task_id) if c.author == "lina-work"]


def test_plan_links_goal_and_writes_card_todos(work, capsys):
    desc = _plan(work, capsys)
    ids = {c["key"]: c["id"] for c in desc["cards"]}
    recs = _record()

    assert f"kanban:{desc['goal']['id']}" in recs["G1"]["links"]
    assert any("G1" in body for body in _comments(desc["goal"]["id"]))
    own, other = (c["ledger_todos"] for c in desc["cards"])
    assert desc["goal"]["ledger_goal"] == "G1"
    assert len(own) == 1 and other[0] == "T1" and len(other) == 2
    assert desc["cards"][1]["ledger_todos_created"] == [other[1]]
    assert desc["cards"][1]["ledger_todos_linked"] == ["T1"]
    for key, todo_id, by, title in (("own", own[0], "alpha", NEW_OWN["title"]),
                                    ("other", other[1], "beta", NEW_OTHER["title"])):
        r = recs[todo_id]
        assert (r["kind"], r["by"], r["parent"], r["title"]) == ("할 일", by, "G1", title)
        assert f"kanban:{ids[key]}" in r["links"]
    assert f"kanban:{ids['other']}" in recs["T1"]["links"]
    assert any(", ".join(other) in body for body in _comments(ids["other"]))


def test_only_other_agents_get_one_line_with_card_and_todo_ids(work, capsys):
    desc = _plan(work, capsys)
    other = desc["cards"][1]

    lines = work.notices("beta").splitlines()
    assert len(lines) == 1
    assert lines[0].split(" ", 1)[1] == (f"[작업 카드] {other['id']} {other['title']} · 할 일 "
                                         + ", ".join(other["ledger_todos"]))
    assert work.notices("alpha") == "" and work.notices("owner") == ""
    assert [n["agent"] for n in desc["notices"]] == ["beta"]


def test_rerun_creates_nothing_and_tells_nobody(work, capsys):
    first = _plan(work, capsys)
    before, beta = _record(), work.notices("beta")

    again = _plan(work, capsys)

    assert not again["created"] and again["notices"] == []
    assert [c["ledger_todos"] for c in again["cards"]] == [c["ledger_todos"] for c in first["cards"]]
    assert _record() == before and work.notices("beta") == beta and len(_tasks()) == 3


def _changed(**change) -> dict:
    plan = copy.deepcopy(PLAN)
    for path, value in change.items():
        target = plan
        *parents, last = path.split("__")
        for part in parents:
            target = target[int(part)] if part.isdigit() else target[part]
        target[last] = value
    return plan


@pytest.mark.parametrize("plan, names", [
    (_changed(goal__ledger_goal="G9"), "goal: ledger_goal G9 is not an open goal in the work record"),
    (_changed(goal__ledger_goal="T1"), "goal: ledger_goal 'T1' must be a goal id `G<n>`"),
    (_changed(goal__ledger_goal=None), "card 'own': ledger_todos needs the goal's ledger_goal"),
    (_changed(cards__0__ledger_todos=["T1"]), "card 'other': ledger to-do T1 is already in card 'own'"),
    (_changed(cards__1__ledger_todos=["T9"]), "card 'other': ledger_todos T9 is not an open to-do under G1"),
    (_changed(cards__0__ledger_todos=[NEW_OWN | {"owner": "x"}]), "card 'own': ledger_todos: unknown field(s) owner"),
    (_changed(cards__0__ledger_todos=[{k: v for k, v in NEW_OWN.items() if k != "why"}]),
     "card 'own': ledger_todos why is required"),
    (_changed(cards__0__ledger_todos=[NEW_OWN | {"check": "echo ok > /tmp/x"}]), "card 'own': new to-do '기기 2 점검':"),
])
def test_refusal_writes_nothing_to_either_store(work, capsys, plan, names):
    before = _record()

    rc, _out, err = _run(work, capsys, plan)

    assert rc != 0
    assert "refused, nothing written" in err and names in err
    assert _tasks() == {} and _record() == before
    assert work.notices("beta") == ""


def test_dry_run_shows_record_side_and_writes_nothing(work, capsys):
    before = _record()

    rc, out, err = _run(work, capsys, PLAN, "--dry-run")

    assert rc == 0, err
    assert "record goal: G1" in out
    assert "to-do: link T1" in out and "to-do: create '기기 3 점검'" in out
    assert "notice  @beta  card other" in out and "notice  @alpha" not in out
    assert _tasks() == {} and _record() == before and work.notices("beta") == ""


def test_failed_record_step_names_it_and_a_rerun_finishes_it(work, capsys, monkeypatch):
    from hermes_cli.work import cards
    real = cards.link_card

    def store_down(card, *, goal=None, todos=None, as_=None):
        if todos:
            print("work: store down", file=sys.stderr)
            raise SystemExit(2)
        return real(card, goal=goal, todos=todos, as_=as_)

    monkeypatch.setattr(cards, "link_card", store_down)
    rc, _out, err = _run(work, capsys, PLAN)
    tasks = _tasks()
    assert rc != 0 and len(tasks) == 3
    assert "the work record step failed at to-dos of card 'own'" in err and "store down" in err
    assert all(task_id in err for task_id in tasks)

    monkeypatch.setattr(cards, "link_card", real)
    desc = _plan(work, capsys)

    assert _tasks().keys() == tasks.keys()
    titles = [r["title"] for r in _record().values() if r["kind"] == "할 일"]
    assert sorted(titles) == sorted(["기기 1 점검", NEW_OWN["title"], NEW_OTHER["title"]])
    assert len(desc["cards"][1]["ledger_todos"]) == 2
    assert len(work.notices("beta").splitlines()) == 1


def test_a_todo_may_name_its_work_card_as_its_run(work):
    out = work.ok("add", "할 일", "--by", "beta", "--goal", "G1", "--title", "기기 4 점검", "--why", "작업이 멈추지 않게",
                  "--done", "기기가 답한다", "--check", "false", "--link", "kanban:t_1234abcd")
    assert "kanban:t_1234abcd" in _record()[out.split()[0]]["links"]

    code, _out, err = work.run("add", "할 일", "--by", "beta", "--goal", "G1", "--title", "기기 5 점검",
                               "--why", "작업이 멈추지 않게", "--done", "기기가 답한다", "--check", "false")
    assert code == 2 and "--link kanban:" in err
