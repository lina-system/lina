"""``lina worker`` called the way the agents call ``omo-task`` today (lina-system/lina#287, #275).

A card session's terminal has no ``HERMES_KANBAN_TASK``: the card comes from the nearest ancestor process that has
it. The owner of a run follows the caller's profile. A card parked with ``kanban_schedule`` that names the run is
woken on its end, with or without ``wait``. A run waiting on a person leaves its question on the card. The hub
node's rules reach its runs, and ``worker.todo_link`` links the to-do. Every Hermes command here is a fake that
records its argv and refuses writes from a delegated child."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import textwrap
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli.worker import device, kanban_link, serve
from hermes_cli.worker import hub as hub_module
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.runners.base import Handle
from hermes_cli.worker.task import write_json
from hermes_cli.worker.transport import Reply
from tests.hermes_cli import _worker_fakes as fakes

CARD, BOARD = "t_card1", "ops"
REPO = Path(__file__).resolve().parents[2]

FAKE_KANBAN = """import json, os, sys
argv = sys.argv[1:]
verb = argv[argv.index("kanban") + 3] if "--board" in argv else argv[argv.index("kanban") + 1]
refused = "HERMES_DELEGATED_CHILD_CONTEXT" in os.environ and verb != "show"
with open(os.environ["FAKE_KANBAN_LOG"], "a") as fh:
    fh.write(json.dumps({"verb": verb, "argv": argv, "ok": not refused}) + "\\n")
if refused:
    print("delegate_task child contexts cannot mutate", file=sys.stderr)
    sys.exit(1)
if verb == "show":
    print(open(os.environ["FAKE_KANBAN_SHOW"]).read())
"""
FAKE_TODO = """import json, os, sys
with open(os.environ["FAKE_TODO_LOG"], "a") as fh:
    fh.write(json.dumps(sys.argv[1:]) + "\\n")
sys.exit(int(os.environ.get("FAKE_TODO_EXIT", "0")))
"""


def _config(base: Path, extra: str = "", devices: str = "{}", runners: str = "{}") -> None:
    base.mkdir(parents=True, exist_ok=True)
    (base / "config.yaml").write_text(
        "devices:\n  workers:\n" + textwrap.indent(devices, "    ") + "\n"
        "worker:\n  poll_s: 0.05\n  min_free_gb: 0\n" + extra + "  runners:\n" + textwrap.indent(runners, "    ")
        + "\n", encoding="utf-8")


@pytest.fixture
def kanban(tmp_path, monkeypatch):
    """A fake ``hermes``: its calls, and ``show(status, events)`` setting what ``kanban show --json`` answers."""
    script, log, shown = tmp_path / "fake_kanban.py", tmp_path / "kanban.jsonl", tmp_path / "show.json"
    script.write_text(FAKE_KANBAN, encoding="utf-8")
    monkeypatch.setenv("FAKE_KANBAN_LOG", str(log))
    monkeypatch.setenv("FAKE_KANBAN_SHOW", str(shown))

    def show(status: str = "scheduled", events: list | None = None) -> None:
        shown.write_text(json.dumps({"task": {"id": CARD, "status": status}, "latest_summary": None, "parents": [],
                                     "children": [], "comments": [], "events": events or [], "runs": []}),
                         encoding="utf-8")

    def calls() -> list[dict]:
        return [json.loads(line) for line in log.read_text(encoding="utf-8").splitlines()] if log.is_file() else []
    show()
    return [sys.executable, str(script)], show, calls


@pytest.fixture
def board(tmp_path, kanban):
    """A hub with a Hermes card link and no devices: runs stay queued and their ends are reported by hand."""
    command, show, calls = kanban
    base = tmp_path / "lina"
    _config(base, f"  card_link: {json.dumps({'kind': 'hermes', 'command': command})}\n")
    hub = Hub(base / "worker", out=lambda *_: None)

    def submit(name: str = "brief.md") -> str:
        lines: list[str] = []
        Hub(hub.worker_root, out=lines.append).submit({
            "briefs": [fakes.brief(tmp_path, "do it", name)], "mode": "ulw", "bot": "agent-a", "topic": "t",
            "wait": None, "card": {"task": CARD, "board": BOARD}})
        hub.tick()  # the start comment
        return lines[0].split()[0]
    return hub, submit, show, calls


def _scheduled(reason: str, workers: list[str] | None = None, at: int = 1793949695) -> dict:
    payload: dict = {"reason": reason}
    if workers is not None:
        payload["goal_park"] = {"session_id": "s-1", "turns_used": 2, "workers": workers, "parked_at": at,
                                "run_id": 7}
    return {"kind": "scheduled", "payload": payload, "created_at": at, "run_id": 7}


def _woke(calls) -> int:
    return sum(1 for call in calls() if call["verb"] == "unblock" and call["ok"])


def test_the_card_comes_from_the_nearest_ancestor_that_has_it_up_to_sixteen_levels():
    table = {50: {"PATH": "/bin"}, 49: {}, 48: {"HERMES_KANBAN_TASK": "t_near", "HERMES_KANBAN_BOARD": "b1"},
             47: {"HERMES_KANBAN_TASK": "t_far"}}
    parents = {50: 49, 49: 48, 48: 47, 47: 1}
    found = kanban_link.card_from_ancestors(50, env_of=lambda pid: table.get(pid, {}), parent_of=parents.get)
    assert found == {"task": "t_near", "board": "b1"}

    chain = {pid: pid - 1 for pid in range(100, 70, -1)}
    far = {100 - kanban_link.ANCESTOR_LEVELS: {"HERMES_KANBAN_TASK": "t_too_far"}}
    assert kanban_link.card_from_ancestors(100, env_of=lambda pid: far.get(pid, {}), parent_of=chain.get) is None
    near = {101 - kanban_link.ANCESTOR_LEVELS: {"HERMES_KANBAN_TASK": "t_last"}}
    assert kanban_link.card_from_ancestors(100, env_of=lambda pid: near.get(pid, {}),
                                           parent_of=chain.get) == {"task": "t_last", "board": None}


def _lina_env(base: Path, **extra: str) -> dict:
    env = {key: value for key, value in os.environ.items() if not key.startswith(("HERMES_KANBAN_", "HERMES_PROFILE"))}
    env.update(LINA_HOME=str(base), PYTHONPATH=str(REPO), HERMES_HOME=str(base), **extra)
    env.pop("HERMES_DELEGATED_CHILD_CONTEXT", None)
    return env


@pytest.mark.platforms("posix")
def test_submit_send_and_wait_in_a_card_terminal_take_the_card_of_the_ancestor_process(tmp_path, kanban):
    """The card's worker process has the card in its environment; the terminal it starts does not."""
    command, _, _ = kanban
    base = tmp_path / "lina"
    _config(base, f"  card_link: {json.dumps({'kind': 'hermes', 'command': command})}\n")
    brief = fakes.brief(tmp_path, "do it")
    other = Hub(base / "worker", out=lambda *_: None)
    lines: list[str] = []
    Hub(base / "worker", out=lines.append).submit({"briefs": [brief], "mode": "ulw", "bot": "agent-a", "topic": "t",
                                                   "wait": None, "card": {"task": "t_other", "board": BOARD}})
    foreign = lines[0].split()[0]
    terminal = textwrap.dedent(f"""\
        import os, subprocess, sys
        env = {{k: v for k, v in os.environ.items() if not k.startswith("HERMES_KANBAN_")}}
        def lina(*args):
            done = subprocess.run([sys.executable, "-m", "hermes_cli.main", "worker", *args], env=env,
                                  capture_output=True, text=True)
            print(done.returncode, done.stdout.replace("\\n", " | "), done.stderr.replace("\\n", " | "), flush=True)
        lina("submit", "--brief", {brief!r}, "--topic", "t")
        lina("wait")
        lina("send", {foreign!r}, "more")
        """)
    worker = _lina_env(base, HERMES_KANBAN_TASK=CARD, HERMES_KANBAN_BOARD=BOARD)
    done = subprocess.run([sys.executable, "-c", terminal], env=worker, capture_output=True, text=True, timeout=300,
                          check=False)
    submitted, waited, sent = done.stdout.splitlines()
    assert submitted.startswith("0 "), done.stdout + done.stderr
    jobs = [job for job in other.store.all() if job["id"] != foreign]
    assert [job["card"] for job in jobs] == [{"task": CARD, "board": BOARD, "kind": "hermes"}]
    assert waited.startswith("0 ") and jobs[0]["id"] in waited and "kanban_schedule" in waited
    assert sent.startswith("2 ") and f"does not belong to card {CARD} on board {BOARD}" in sent


def _parse(argv: list[str]) -> argparse.Namespace:
    from hermes_cli.subcommands.worker import build_worker_parser
    root = argparse.ArgumentParser(prog="lina")
    build_worker_parser(root.add_subparsers(dest="command"))
    return root.parse_args(argv)


def test_card_board_is_the_board_of_card_and_submit_session_is_accepted():
    for argv in (["worker", "submit", "--brief", "b.md", "--card", CARD, "--card-board", BOARD, "--session", "s1"],
                 ["worker", "send", "OT-X1", "more", "--card", CARD, "--card-board", BOARD],
                 ["worker", "wait", "--card", CARD, "--card-board", BOARD]):
        parsed = _parse(argv)
        assert (parsed.card, parsed.board) == (CARD, BOARD)


@pytest.mark.parametrize(("env", "owner"), [
    ({"HERMES_PROFILE": "p-one", "HERMES_PROFILE_NAME": "p-two", "caller": "profiles/p-three"}, "p-one"),
    ({"HERMES_PROFILE_NAME": "p-two", "caller": "profiles/p-three"}, "p-two"),
    ({"caller": "profiles/p-three"}, "p-three"),
    ({"caller": ""}, "default"),
    ({}, "the-active-one"),
])
def test_the_owner_follows_the_caller_and_the_verdict_names_the_same(tmp_path, monkeypatch, env, owner):
    base, hermes = tmp_path / "lina", tmp_path / "hermes-root"
    _config(base)
    for name in ("HERMES_PROFILE", "HERMES_PROFILE_NAME", "LINA_CALLER_HERMES_HOME"):
        monkeypatch.delenv(name, raising=False)
    for name, value in env.items():
        if name == "caller":
            monkeypatch.setenv("LINA_CALLER_HERMES_HOME", str(hermes / value) if value else str(hermes))
        else:
            monkeypatch.setenv(name, value)
    monkeypatch.setattr(hub_module, "caller_agent", lambda: "the-active-one")
    monkeypatch.setattr(kanban_link, "card_from_ancestors", lambda *a, **k: None, raising=False)
    lines: list[str] = []
    hub = Hub(base / "worker", out=lines.append)
    hub.submit({"briefs": [fakes.brief(tmp_path, "do it")], "mode": "ulw", "topic": "t", "wait": None})
    task_id = lines[0].split()[0]
    assert hub.store.load(task_id)["bot"] == owner
    hub.verdict(task_id, True, "checked")
    hub.verdict(task_id, False, "missing a test")
    assert [r["by"] for r in hub.store.receipts_of(task_id) if r["kind"] == "verdict"] == [owner, owner]
    hub.submit({"briefs": [fakes.brief(tmp_path, "do it")], "mode": "ulw", "topic": "t", "wait": None,
                "bot": "named"})
    assert hub.store.load(lines[-2].split()[0])["bot"] == "named"


def test_a_card_scheduled_on_a_run_is_woken_by_its_end_without_wait(board):
    hub, submit, show, calls = board
    run = submit()
    show("scheduled", [{"kind": "created", "payload": {}}, _scheduled(f"WAIT_EVENT=omo-task:{run}")])
    hub.notify(run, "done", "1")
    hub.notify(run, "done", "1")
    assert [call["verb"] for call in calls()][1:] == ["comment", "show", "unblock"]


def test_a_schedule_on_several_runs_is_woken_once_after_the_last_of_them(board):
    hub, submit, show, calls = board
    first, second = submit("a.md"), submit("b.md")
    show("scheduled", [_scheduled(f"waiting on lina worker {first}", [first, second])])
    hub.notify(first, "done", "1")
    assert _woke(calls) == 0
    hub.notify(second, "failed", "1")
    assert _woke(calls) == 1
    third = submit("c.md")  # a later run of the same card, while the card has not scheduled itself again
    hub.notify(third, "done", "1")
    assert _woke(calls) == 1


def test_a_schedule_that_names_a_run_this_hub_does_not_know_is_left_to_the_tool_that_started_it(board):
    hub, submit, show, calls = board
    run = submit()
    assert hub.wait({"task": CARD, "board": BOARD}) == 0
    show("scheduled", [_scheduled(f"workers={run},OT-OLD1106000000")])
    hub.notify(run, "done", "1")
    assert _woke(calls) == 0
    assert not hub.store.load(run).get("card_pending")  # the end is on the card; nothing is retried


def test_a_run_id_is_found_as_a_whole_token_in_any_wording(board):
    hub, submit, show, calls = board
    run, other = submit("a.md"), submit("b.md")
    show("scheduled", [_scheduled(f"{other}-2 기다림")])  # another run's ID, not this one's
    hub.notify(other, "done", "1")
    assert _woke(calls) == 0
    show("scheduled", [_scheduled(f"작업 {run}를 기다린다")])
    hub.notify(run, "done", "1")
    assert _woke(calls) == 1


def test_a_card_still_running_when_its_run_ends_is_woken_on_a_later_tick(board):
    hub, submit, show, calls = board
    run = submit()
    show("running", [_scheduled(f"workers={run}")])
    hub.notify(run, "done", "1")
    assert _woke(calls) == 0 and hub.store.load(run).get("card_pending")
    show("scheduled", [_scheduled(f"workers={run}")])
    hub.tick()
    hub.tick()
    assert _woke(calls) == 1 and not hub.store.load(run).get("card_pending")


@pytest.fixture
def asking(tmp_path, kanban, monkeypatch):
    """A run on a Hermes card waiting on a person's answer, its device played in-process."""
    command, _, calls = kanban
    base = tmp_path / "lina"
    _config(base, f"  card_link: {json.dumps({'kind': 'hermes', 'command': command})}\n",
            devices="here: {host: local, cap: 1}")
    root, tid = base / "worker", "OT-AGE1106000001"
    task = device.task_of(root, tid)
    task.work.mkdir(parents=True)
    for name, text in (("prompt.1", "task"), ("request.1", '{"run": 1}'), ("state", "running")):
        task.path(name).write_text(text, encoding="utf-8")
    task.path("taken.1").touch()
    write_json(task.path("live.1.json"), {"state": "waiting_human"})
    write_json(task.path("job.json"), {"runner": "fake", "access": "workspace-write"})
    Handle(task, 1).save()
    runner = SimpleNamespace(honours=lambda access: (True, ""))
    monkeypatch.setattr(device, "alive", lambda *args: True)
    monkeypatch.setattr(device, "runner_for", lambda *args: runner)
    hub = Hub(root, out=lambda text: None)
    hub.store.save({"id": tid, "host": "here", "state": "running", "runs": 1, "created": 1, "access": "workspace-write",
                    "grant": {"access": "workspace-write"}, "bot": "agent-a", "topic": "t", "notified": [],
                    "human_since": 1, "card": {"task": CARD, "board": BOARD, "kind": "hermes"}})
    monkeypatch.setattr(hub, "transport", lambda job: SimpleNamespace(
        call=lambda argv, stdin=b"", timeout=90: Reply(*device.dispatch(root, argv, stdin), "")))
    monkeypatch.setattr(hub_module.time, "time", lambda: 1000)  # past the wait before a question is told

    def comments() -> list[str]:
        return [call["argv"][-1] for call in calls() if call["verb"] == "comment" and call["ok"]]
    return hub, task, runner, comments, calls


def test_a_question_of_a_command_runner_is_written_once_on_its_card(asking):
    hub, task, _, comments, calls = asking
    (task.work / "question.md").write_text("Which colour should ok.txt say?\n", encoding="utf-8")
    hub.tick()
    hub.tick()
    hub.tick()
    assert len(comments()) == 1 and "Which colour should ok.txt say?" in comments()[0]
    assert f"lina worker send {task.id}" in comments()[0]
    assert [call["verb"] for call in calls()] == ["comment"]  # no attachment, no wake
    (task.work / "question.md").write_text("And which size?\n", encoding="utf-8")
    hub.tick()
    hub.tick()
    assert len(comments()) == 2 and "And which size?" in comments()[1]


def test_a_question_of_the_omo_runner_is_written_once_on_its_card(asking):
    hub, _, runner, comments, _ = asking
    runner.live_view = lambda handle: {"dash": "human", "questions": [
        {"id": "q-1", "requestId": "req-1", "questions": [{"question": "Ship it on Friday?"}]}]}
    hub.tick()
    hub.tick()
    hub.tick()
    assert len(comments()) == 1 and "Ship it on Friday?" in comments()[0]


@pytest.mark.parametrize("rules", ["- Never open the owner's key files.", None])
def test_a_run_on_the_hub_node_gets_the_hub_rules_after_the_standard_rules(tmp_path, monkeypatch, rules):
    base = tmp_path / "lina"
    argv = fakes.install_fake_cli(tmp_path / "fake")
    runner = json.dumps({"type": "command", "argv": argv, "access": "workspace-write"})
    _config(base, f"  hub_rules: {json.dumps(rules)}\n" if rules else "", devices="here: {host: local, cap: 1}",
            runners=f"fakecli: {runner}")
    monkeypatch.setattr(kanban_link, "card_from_ancestors", lambda *a, **k: None, raising=False)
    root = base / "worker"
    serve.serve_once(root)
    lines: list[str] = []
    hub = Hub(root, out=lines.append)
    hub.submit({"briefs": [fakes.brief(tmp_path, "do it")], "mode": "ulw", "bot": "agent-a", "topic": "t",
                "runner": "fakecli", "wait": None})
    task_id = lines[0].split()[0]
    job = hub.store.load(task_id)
    assert job["host"] == "here"
    prompt = device.task_of(root, task_id).path("prompt.1").read_text(encoding="utf-8")
    if rules:
        assert prompt.startswith(job["prompt"].rstrip("\n")) and prompt.rstrip("\n").endswith(rules)
    else:
        assert prompt == job["prompt"]


def _cli(argv: list[str]) -> int:
    from hermes_cli.subcommands.worker import _cmd_worker
    with pytest.raises(SystemExit) as ended:
        _cmd_worker(_parse(["worker", *argv]))
    return ended.value.code


def test_attach_prints_the_status_and_card_shows_the_card_and_refuses_changes(tmp_path, monkeypatch, capsys, board):
    hub, submit, _, _ = board
    run = submit()
    monkeypatch.setenv("LINA_HOME", str(hub.base))
    assert _cli(["attach", run]) == 0
    assert capsys.readouterr().out.startswith(f"{run} · agent-a · t")
    assert _cli(["card", run]) == 0
    assert capsys.readouterr().out.split() == [CARD, BOARD, "(hermes)"]
    for change in (["card", run, "t_other"], ["card", run, "--drop", CARD]):
        assert _cli(change) == 2
        printed = capsys.readouterr()
        assert printed.out == "" and printed.err.count("\n") == 1 and "cannot be added or dropped" in printed.err
    assert hub.store.load(run)["card"]["task"] == CARD


@pytest.fixture
def todo(tmp_path, monkeypatch):
    script, log = tmp_path / "fake_todo.py", tmp_path / "todo.jsonl"
    script.write_text(FAKE_TODO, encoding="utf-8")
    monkeypatch.setenv("FAKE_TODO_LOG", str(log))
    monkeypatch.setattr(kanban_link, "card_from_ancestors", lambda *a, **k: None, raising=False)
    base = tmp_path / "lina"
    _config(base, f"  todo_link: {json.dumps({'kind': 'hermes', 'command': [sys.executable, str(script)]})}\n")
    lines: list[str] = []
    hub = Hub(base / "worker", out=lines.append)

    def calls() -> list[list[str]]:
        return [json.loads(line) for line in log.read_text(encoding="utf-8").splitlines()] if log.is_file() else []
    return hub, lines, calls


def test_a_submit_with_a_todo_links_it_and_an_accept_records_evidence_and_the_note(tmp_path, todo):
    hub, lines, calls = todo
    assert hub.submit({"briefs": [fakes.brief(tmp_path, "do it")], "mode": "ulw", "bot": "agent-a",
                       "todo": "T-100", "wait": None}) == 0
    task_id = lines[0].split()[0]
    assert calls() == [["link", "T-100", f"omo:{task_id}"]]
    assert hub.verdict(task_id, True, "tests pass") == 0
    assert calls()[1:] == [["evidence", "T-100", f"omo:{task_id}"], ["note", "T-100", f"{task_id} checked: tests pass"]]
    assert hub.verdict(task_id, False, "no") == 0
    assert len(calls()) == 3


def test_a_todo_command_that_fails_prints_one_line_and_fails_nothing(tmp_path, todo, monkeypatch):
    hub, lines, calls = todo
    monkeypatch.setenv("FAKE_TODO_EXIT", "3")
    assert hub.submit({"briefs": [fakes.brief(tmp_path, "do it")], "mode": "ulw", "bot": "agent-a",
                       "todo": "T-100", "wait": None}) == 0
    task_id = lines[0].split()[0]
    failed = [line for line in lines if "T-100" in line and "failed" in line]
    assert len(failed) == 1 and "\n" not in failed[0]
    lines.clear()
    assert hub.verdict(task_id, True, "") == 0
    assert hub.store.load(task_id)["verdict"] == "accepted"
    assert lines[0] == f"{task_id} accepted" and len(lines) == 2
    assert calls()[-1] == ["evidence", "T-100", f"omo:{task_id}"]
