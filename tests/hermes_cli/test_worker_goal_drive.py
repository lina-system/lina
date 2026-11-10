"""The goal drive (lina#225): rules the hub's tick runs on the runs of a goal's work cards.

The hub and the devices are real (two local devices, ``device.launch`` replaced so no runner starts). The Hermes
kanban is a fake command that answers ``show`` from card files and records every call; ``gh`` is a fake on PATH that
answers from a rules file."""

import json
import os
import sys
import time
from types import SimpleNamespace

import pytest

from hermes_cli import lina_work_settings as ws
from hermes_cli.worker import device, goal_drive, hub as hub_mod, kanban_link, serve
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.transport import Reply
from tests.hermes_cli import _worker_fakes as fakes

BOARD, GOAL, WORK = "ops", "t_goal", "t_work1"
SHA = "a" * 40

FAKE_KANBAN = """import json, os, sys
args = sys.argv[sys.argv.index("kanban") + 1:]
if args[0] == "--board":
    args = args[2:]
if args[0] == "unblock" and args[1] == "--judge-wake":  # unblock --judge-wake <run> <card> ...
    args = [args[0], args[3], *args[1:3], *args[4:]]
verb, task, rest = args[0], args[1], args[2:]
cards = os.environ["FAKE_CARDS"]
with open(os.path.join(cards, "calls.jsonl"), "a") as fh:
    fh.write(json.dumps([verb, task, *rest]) + "\\n")
if verb == "schedule" and "--wait-for" in rest and os.environ.get("FAKE_NO_WAIT_FOR"):  # the installed Hermes
    print("hermes: error: unrecognized arguments: --wait-for", file=sys.stderr)
    sys.exit(2)
path = os.path.join(cards, task + ".json")
card = json.load(open(path)) if os.path.exists(path) else {"title": task, "status": "running", "body": ""}
# The transitions of the real board: block from running/ready, schedule from todo/ready/running/blocked, unblock
# from blocked/scheduled, promote from todo/blocked; any other is refused with exit 1.
moves = {"block": (("running", "ready"), "blocked"), "schedule": (("todo", "ready", "running", "blocked"), "scheduled"),
         "unblock": (("blocked", "scheduled"), "ready"), "promote": (("todo", "blocked"), "ready")}
if verb == "show":
    events = card.pop("events", [])
    print(json.dumps({"task": card, "events": events}))
elif verb in moves and os.path.exists(path):
    sources, target = moves[verb]
    if card["status"] not in sources:
        print(f"cannot {verb} {task}", file=sys.stderr)
        sys.exit(1)
    card["status"] = target
    if verb == "schedule":
        card.setdefault("events", []).append({"kind": "scheduled", "payload": {"reason": rest[-1]}})
    json.dump(card, open(path, "w"))
"""

FAKE_GH = """import json, os, re, sys
here = os.environ["FAKE_GH_DIR"]
line = " ".join(sys.argv[1:])
with open(os.path.join(here, "calls.jsonl"), "a") as fh:
    fh.write(json.dumps(line) + "\\n")
for pattern, out, code in json.load(open(os.path.join(here, "rules.json"))):
    if re.search(pattern, line):
        sys.stdout.write(out if isinstance(out, str) else json.dumps(out))
        sys.exit(code)
print("no fake answer", file=sys.stderr)
sys.exit(1)
"""


def _card(cards, task: str, title: str, status: str, body: str = "") -> None:
    (cards / f"{task}.json").write_text(json.dumps({"title": title, "status": status, "body": body}))


@pytest.fixture
def drive(tmp_path, monkeypatch):
    for name in [name for name in os.environ if name.startswith("HERMES_KANBAN_")]:
        monkeypatch.delenv(name)
    monkeypatch.setattr(kanban_link, "card_from_ancestors", lambda *a, **k: None)
    base, fake, cards, gh = tmp_path / "lina", tmp_path / "fake", tmp_path / "cards", tmp_path / "gh"
    for folder in (cards, gh, tmp_path / "bin"):
        folder.mkdir()
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    monkeypatch.setenv("LINA_HOME", str(base))
    monkeypatch.setenv("FAKE_CARDS", str(cards))
    monkeypatch.setenv("FAKE_GH_DIR", str(gh))
    script = tmp_path / "fake_kanban.py"
    script.write_text(FAKE_KANBAN)
    gh_bin = tmp_path / "bin" / "gh"
    gh_bin.write_text(f"#!{sys.executable}\n" + FAKE_GH)
    gh_bin.chmod(0o755)
    monkeypatch.setenv("PATH", f"{tmp_path / 'bin'}{os.pathsep}{os.environ['PATH']}")
    (gh / "rules.json").write_text("[]")
    spec = {"type": "command", "argv": argv, "access_args": {mode: [] for mode in hub_mod.ACCESS}}

    def configure(extra: str = "", there: str = "{host: local, cap: 4}", hermes: bool = True) -> None:
        base.mkdir(exist_ok=True)
        link = {"kind": "hermes", "command": [sys.executable, str(script)]} if hermes else {"kind": "lina"}
        (base / "config.yaml").write_text(
            f"devices:\n  workers:\n    here: {{host: local, cap: 4}}\n    there: {there}\n"
            "worker:\n  poll_s: 0.05\n  stall_grace_s: 30\n  answered_grace_s: 0.2\n  min_free_gb: 0\n  min_free_mem_gb: 0\n"
            f"  card_link: {json.dumps(link)}\n"
            f"  runners:\n    fakecli: {json.dumps(spec)}\n" + extra)
    configure()
    launched: list[tuple[str, int]] = []
    monkeypatch.setattr(device, "launch", lambda root, task, run: launched.append((task.id, run)))
    root = base / "worker"
    serve.serve_once(root)
    _card(cards, GOAL, "Ship the thing", "todo")
    _card(cards, WORK, "Build it", "scheduled",
          f"## Goal\n\n{GOAL} — Ship the thing\n\n## To-dos\n\n- acme/app#1\n")
    return SimpleNamespace(root=root, tmp=tmp_path, cards=cards, gh=gh, mp=monkeypatch, configure=configure,
                           launched=launched)


def _hub(drive) -> Hub:
    return Hub(drive.root, out=lambda *_: None)


def _submit(drive, todo: str = "acme/app#1", host: str = "here", **opts) -> str:
    hub = _hub(drive)
    before = {job["id"] for job in hub.store.all()}
    hub.submit({"briefs": [fakes.brief(drive.tmp, f"SCENARIO:done {todo}", f"brief-{len(before)}.md")], "mode": "ulw",
                "todo": todo, "bot": "tester", "runner": "fakecli", "wait": None, "host": host,
                "card": {"task": WORK, "board": BOARD}, **opts})
    (new,) = {job["id"] for job in hub.store.all()} - before
    return new


def _end(drive, task_id: str, state: str, report: bool = False) -> None:
    """The run the task holds ended with *state* on its device, which reported it to the hub (the end lands on the
    card, whose view the hub keeps)."""
    hub = _hub(drive)
    run = hub.store.load(task_id)["runs"]
    folder = drive.root / "tasks" / task_id
    (folder / f"rc.{run}").write_text("0\n")
    (folder / "state").write_text(state + "\n")
    hub.notify(task_id, state, str(run))
    assert not hub.store.load(task_id).get("card_pending")
    if report:
        out = hub.store.out_dir(hub.store.load(task_id))
        out.mkdir(parents=True, exist_ok=True)
        (out / "report.md").write_text("# report\n")


def _requests(drive, task_id: str) -> list[str]:
    return sorted(p.name for p in (drive.root / "tasks" / task_id).glob("request.*"))


def _kanban(drive) -> list[list[str]]:
    path = drive.cards / "calls.jsonl"
    return [json.loads(line) for line in path.read_text().splitlines()] if path.is_file() else []


def _writes(drive, verb: str, task: str | None = None) -> list[list[str]]:
    return [call for call in _kanban(drive) if call[0] == verb and (task is None or call[1] == task)]


def _ledger(drive) -> dict:
    return json.loads((drive.root / "hub" / "goals" / "t-goal.json").read_text())


def _aid(task: str, run: int, rule: str) -> str:
    return f"t-goal:t-work1:{task}:{run}:{rule}"


def _tick(drive) -> None:
    _hub(drive).tick()


def _never_done(drive) -> None:
    assert not _writes(drive, "complete") and not _writes(drive, "unblock", GOAL)


def test_a_run_without_its_deliverable_gets_one_follow_up_and_a_restarted_hub_repeats_nothing(drive):
    task = _submit(drive)
    _end(drive, task, "done")
    _tick(drive)
    assert _requests(drive, task) == ["request.1", "request.2"]
    assert "without its deliverable" in (drive.root / "tasks" / task / "prompt.2").read_text()
    assert list(_ledger(drive)["actions"]) == [_aid(task, 1, "deliverable")]
    _tick(drive)
    _tick(drive)  # every _tick is a new Hub on the same root
    assert _requests(drive, task) == ["request.1", "request.2"]
    assert [r["kind"] for r in _hub(drive).store.receipts_of(task)].count("follow_up") == 1
    _never_done(drive)


def test_a_follow_up_whose_reply_was_lost_is_sent_again_under_its_action_id_and_runs_once(drive):
    task = _submit(drive)
    _end(drive, task, "done")
    real, dropped = hub_mod.for_device, []

    def lossy(dev, root):
        transport = real(dev, root)
        call = transport.call

        def maybe_drop(verb, stdin=b"", timeout=120):
            reply = call(verb, stdin, timeout)
            if verb[0] == "follow_up" and not dropped:
                dropped.append(verb)
                return Reply(255, b"", "connection reset by peer")
            return reply
        transport.call = maybe_drop
        return transport
    drive.mp.setattr(hub_mod, "for_device", lossy)
    _tick(drive)
    assert dropped and _ledger(drive)["actions"] == {}
    _tick(drive)
    assert _requests(drive, task) == ["request.1", "request.2"]
    replayed = [r for r in _hub(drive).store.receipts_of(task) if r["kind"] == "request_replayed"]
    assert [r["key"] for r in replayed] == [_aid(task, 1, "deliverable")]
    assert _hub(drive).store.load(task)["runs"] == 2


def _red_check(drive, conclusion: str = "failure", status: str = "completed") -> None:
    log = "\n".join(f"log line {i}" for i in range(100)) + "\n"
    rules = [
        [r"^pr list", [{"number": 7, "url": "https://github.com/acme/app/pull/7", "state": "OPEN"}], 0],
        [r"^api graphql", {"data": {"repository": {"pullRequest": {
            "headRefOid": SHA, "baseRefName": "dev", "state": "OPEN",
            "baseRef": {"branchProtectionRule": {"requiredStatusChecks": [{"context": "tests", "app": None}]}}}}}}, 0],
        [r"rules/branches", [[]], 0],
        [r"check-runs", [{"total_count": 1, "check_runs": [{
            "id": 99, "name": "tests", "app": {"id": 1}, "head_sha": SHA, "status": status, "conclusion": conclusion,
            "html_url": "https://github.com/acme/app/runs/99", "started_at": "2026-01-01T00:00:00Z"}]}], 0],
        [r"/statuses", [[]], 0],
        [r"actions/jobs/99/logs", log, 0],
        [r"pulls/7", {"head": {"sha": SHA}, "base": {"ref": "dev"}, "state": "open"}, 0],
    ]
    (drive.gh / "rules.json").write_text(json.dumps(rules))


def _gh_calls(drive, pattern: str) -> int:
    path = drive.gh / "calls.jsonl"
    return sum(1 for line in path.read_text().splitlines() if pattern in json.loads(line)) if path.is_file() else 0


def test_a_red_required_check_gets_one_follow_up_naming_it_with_the_tail_of_its_log(drive):
    _red_check(drive)
    task = _submit(drive, repo="acme/app")
    _end(drive, task, "done", report=True)
    _tick(drive)
    prompt = (drive.root / "tasks" / task / "prompt.2").read_text()
    assert "`tests`" in prompt and "#7" in prompt
    assert "log line 20\n" in prompt and "log line 99" in prompt and "log line 19\n" not in prompt
    _end(drive, task, "done", report=True)  # run 2 ended without a push: the head is the same
    _tick(drive)
    _tick(drive)
    assert _requests(drive, task) == ["request.1", "request.2"]
    assert _gh_calls(drive, "api graphql") == 1  # the check read is kept for 60 s
    _never_done(drive)


def test_checks_still_running_park_the_card_on_the_pull_request(drive):
    _red_check(drive, conclusion=None, status="in_progress")
    task = _submit(drive, repo="acme/app")
    _end(drive, task, "done", report=True)
    _tick(drive)
    _tick(drive)
    parks = _writes(drive, "schedule", WORK)
    assert len(parks) == 1 and parks[0][2:4] == ["--wait-for", "pr:acme/app#7"]
    assert _requests(drive, task) == ["request.1"]


def test_after_three_follow_ups_in_one_attempt_the_next_blocks_the_card_with_an_inbox_line(drive):
    task = _submit(drive)
    for _ in range(4):
        _end(drive, task, "done")
        _tick(drive)
    assert _requests(drive, task) == ["request.1", "request.2", "request.3", "request.4"]
    (block,) = _writes(drive, "block", WORK)
    assert block[2:4] == ["--kind", "needs_input"]
    assert block[-1].startswith("Build it · 갈림길 · 3 follow-ups on run ") and "\n" not in block[-1]
    _end(drive, task, "done")
    _tick(drive)
    assert len(_writes(drive, "block")) == 1 and _requests(drive, task)[-1] == "request.4"
    _never_done(drive)


def test_two_to_dos_on_one_card_each_get_their_own_follow_up(drive):
    first, second = _submit(drive), _submit(drive, todo="acme/app#2")
    _end(drive, first, "done")
    _end(drive, second, "done")
    _tick(drive)
    assert _requests(drive, first) == ["request.1", "request.2"]
    assert _requests(drive, second) == ["request.1", "request.2"]
    assert set(_ledger(drive)["actions"]) == {_aid(first, 1, "deliverable"), _aid(second, 1, "deliverable")}


def test_an_action_id_too_long_for_a_request_key_writes_its_task_as_a_hash():
    import hashlib
    import re

    from hermes_cli.worker.request_keys import CALLER_KEY
    task = "OT-" + "A" * 120  # a task ID long enough to pass 128 characters
    aid = goal_drive.action_id("t_goal", "t_work1", task, 2, "deliverable")
    assert re.fullmatch(CALLER_KEY, aid) and len(aid) <= 128
    assert aid == f"t-goal:t-work1:{hashlib.sha256(task.encode()).hexdigest()[:12]}:2:deliverable"
    assert aid != goal_drive.action_id("t_goal", "t_work1", task + "B", 2, "deliverable")


def _set_status(drive, task: str, status: str) -> None:
    path = drive.cards / f"{task}.json"
    path.write_text(json.dumps({**json.loads(path.read_text()), "status": status}))


def test_a_card_closed_after_its_last_run_end_is_not_driven(drive):
    _red_check(drive)
    task = _submit(drive, repo="acme/app")
    _end(drive, task, "done", report=True)  # the kept view says scheduled
    _set_status(drive, WORK, "done")
    shown = len(_writes(drive, "show", WORK))
    _tick(drive)
    assert _requests(drive, task) == ["request.1"]
    assert _ledger(drive)["notes"][WORK] == "skipped: card done"
    assert len(_writes(drive, "show", WORK)) - shown == 1


def _review(drive, task_id: str, key: str) -> None:
    """The card session sends a review round (``lina worker send --key``) to the task's ended run."""
    assert _hub(drive).send(task_id, "Codex review: fix the findings below.", request_key=key) == 0


def test_review_rounds_from_the_card_session_count_toward_the_follow_up_limit(drive):
    (drive.gh / "rules.json").write_text(json.dumps([[r"^pr list", [], 0]]))
    task = _submit(drive, repo="acme/app")
    for _ in range(2):
        _end(drive, task, "done")
        _tick(drive)
    _end(drive, task, "done", report=True)
    _review(drive, task, "rk-review-1")
    assert _requests(drive, task) == ["request.1", "request.2", "request.3", "request.4"]
    _red_check(drive)
    real = time.time
    drive.mp.setattr(goal_drive.time, "time", lambda: real() + 61)  # past the cached "no pull request" read
    _end(drive, task, "done", report=True)
    _tick(drive)
    assert _requests(drive, task) == ["request.1", "request.2", "request.3", "request.4"]
    (block,) = _writes(drive, "block", WORK)
    assert "3 follow-ups on run" in block[-1] and "the last from the card session, now: red-check" in block[-1]


def test_a_steer_into_a_live_run_is_not_a_follow_up_round(drive):
    task = _submit(drive)
    _end(drive, task, "done")
    _tick(drive)
    _end(drive, task, "done", report=True)
    _review(drive, task, "rk-review-1")
    _hub(drive).store.receipt(task, "steer", run=3, response={"action": "steered", "run": 3})  # as Hub._steered
    _end(drive, task, "done")
    _tick(drive)
    assert _requests(drive, task) == ["request.1", "request.2", "request.3", "request.4"]
    assert not _writes(drive, "block")
    _end(drive, task, "done")
    _tick(drive)
    assert _requests(drive, task)[-1] == "request.4" and len(_writes(drive, "block", WORK)) == 1


def test_a_fourth_round_sent_by_the_card_session_blocks_the_card_on_the_next_tick(drive):
    task = _submit(drive)
    for _ in range(3):
        _end(drive, task, "done")
        _tick(drive)
    _end(drive, task, "done", report=True)
    _tick(drive)
    assert not _writes(drive, "block")
    _review(drive, task, "rk-review-4")
    assert _requests(drive, task)[-1] == "request.5"
    _tick(drive)
    (block,) = _writes(drive, "block", WORK)
    assert block[-1].startswith("Build it · 갈림길 · 4 follow-ups on run ")
    assert "the last from the card session)" in block[-1]
    _tick(drive)
    assert len(_writes(drive, "block")) == 1


def _outage(drive, on: bool) -> None:
    """While on, every device is unreachable (the hub's ``meta`` gets no reply)."""
    if not hasattr(drive, "down"):
        real = Hub.meta
        drive.mp.setattr(Hub, "meta", lambda self, job, live=False: {"state": "unknown", "unreachable": True}
                         if drive.down else real(self, job, live))
    drive.down = on


def _lose_node(drive) -> None:
    """The node becomes unreachable and stays so for ``node_watch.FAILED_CONTACTS`` ticks: it sends
    ``host.unavailable`` and its running runs become ``unknown``."""
    from hermes_cli.worker import node_watch
    _outage(drive, True)
    for _ in range(node_watch.FAILED_CONTACTS):
        _tick(drive)


def test_two_unreachable_ticks_are_not_a_lost_node_and_the_drive_does_nothing(drive):
    # A hub before lina#223 ended the run as lost after this many seconds unreachable; the key is gone now.
    drive.configure("  unknown_after_s: 0\n")
    task = _submit(drive)
    _outage(drive, True)
    _tick(drive)
    _tick(drive)
    hub = _hub(drive)
    assert hub.store.load(task)["state"] == "running"
    assert hub.store.load_node("here")["failures"] == 2 and not hub.store.load_node("here")["notified_outage"]
    assert not _writes(drive, "schedule") and not _writes(drive, "block")
    assert [job["id"] for job in hub.store.all()] == [task]
    assert not (drive.root / "hub" / "goals" / "t-goal.json").exists() or _ledger(drive)["actions"] == {}


def test_a_lost_node_waits_while_the_outage_is_open_and_moves_once_after_the_run_did_not_continue(drive):
    task = _submit(drive)
    _lose_node(drive)
    _tick(drive)  # the outage is still open
    hub = _hub(drive)
    assert hub.store.load(task)["state"] == "unknown"
    assert hub.store.load_node("here")["notified_outage"]
    assert [job["id"] for job in hub.store.all()] == [task]
    assert [call[2] for call in _writes(drive, "schedule", WORK)] == ["--wait-for"]
    _outage(drive, False)
    (drive.root / "tasks" / task / "rc.1").write_text("1\n")
    (drive.root / "tasks" / task / "state").write_text("failed\n")
    _tick(drive)
    _tick(drive)
    jobs = _hub(drive).store.all()
    assert jobs[0]["id"] == task and len(jobs) == 2
    moved = jobs[1]
    assert (moved["host"], moved["todo"], moved["card"]["task"]) == ("there", "acme/app#1", WORK)
    assert _ledger(drive)["attempts"][moved["id"]] == 2
    assert moved["request_key"] == _aid(task, 1, "resubmit")


def test_a_lost_node_run_is_not_moved_to_a_node_that_takes_no_automatic_work(drive):
    drive.configure(there="{host: local, cap: 4, auto: false}")
    task = _submit(drive)
    _lose_node(drive)
    _outage(drive, False)
    (drive.root / "tasks" / task / "rc.1").write_text("1\n")
    (drive.root / "tasks" / task / "state").write_text("failed\n")
    _tick(drive)
    assert [job["id"] for job in _hub(drive).store.all()] == [task]
    assert "there: only when named with --host" in _ledger(drive)["notes"][task]


def test_a_lost_node_whose_run_went_on_is_not_moved(drive):
    task = _submit(drive)
    _lose_node(drive)
    _outage(drive, False)
    (drive.root / "tasks" / task / "state").write_text("running\n")
    _tick(drive)
    _tick(drive)
    assert [job["id"] for job in _hub(drive).store.all()] == [task]
    assert _aid(task, 1, "node-continued") in _ledger(drive)["actions"]


def test_a_lost_node_is_not_moved_while_another_run_of_the_same_to_do_is_live(drive):
    task = _submit(drive)
    _lose_node(drive)
    _outage(drive, False)
    (drive.root / "tasks" / task / "state").write_text("failed\n")
    (drive.root / "tasks" / task / "rc.1").write_text("1\n")
    other = _submit(drive, host="there")
    _hub(drive).store.update(other, created=_hub(drive).store.load(task)["created"] - 1)  # the lost one is newest
    _tick(drive)
    assert sorted(job["id"] for job in _hub(drive).store.all()) == sorted([task, other])


def test_a_usage_limit_waits_without_a_new_attempt_then_the_run_goes_on(drive):
    task = _submit(drive)
    _end(drive, task, "failed")
    hub = _hub(drive)
    out = hub.store.out_dir(hub.store.load(task))
    out.mkdir(parents=True, exist_ok=True)
    (out / "stderr.txt").write_text("Error 429: usage limit reached, resets in 30 minutes\n")
    _tick(drive)
    _tick(drive)
    (park,) = _writes(drive, "schedule", WORK)
    assert park[3].startswith("until:")
    assert _requests(drive, task) == ["request.1"]
    ledger = _ledger(drive)
    assert ledger["attempts"] == {task: 1} and ledger["followups"] == {}
    real = time.time
    drive.mp.setattr(goal_drive.time, "time", lambda: real() + 1801)
    _tick(drive)
    _tick(drive)
    assert _requests(drive, task) == ["request.1", "request.2"]
    assert "usage limit has reset" in (drive.root / "tasks" / task / "prompt.2").read_text()
    ledger = _ledger(drive)
    assert ledger["attempts"] == {task: 1} and ledger["followups"] == {}
    assert [job["id"] for job in _hub(drive).store.all()] == [task]


def _usage_end(drive, task_id: str) -> None:
    _end(drive, task_id, "failed")
    hub = _hub(drive)
    out = hub.store.out_dir(hub.store.load(task_id))
    out.mkdir(parents=True, exist_ok=True)
    (out / "stderr.txt").write_text("Error 429: usage limit reached, resets in 30 minutes\n")


def test_a_moved_task_that_hits_a_usage_limit_again_is_moved_again_under_its_own_action_id(drive):
    drive.configure("  goal_limits: {usage_move: true}\n")
    first = _submit(drive)
    _usage_end(drive, first)
    _tick(drive)
    second = _hub(drive).store.all()[1]
    assert second["host"] == "there"
    _usage_end(drive, second["id"])
    _tick(drive)
    jobs = _hub(drive).store.all()
    assert len(jobs) == 3 and jobs[2]["host"] == "here"  # moved again, not taken for the first task's move
    assert [job["request_key"] for job in jobs[1:]] == [_aid(first, 1, "usage-move"),
                                                       _aid(second["id"], 1, "usage-move")]
    assert _ledger(drive)["attempts"] == {job["id"]: 1 for job in jobs}  # a usage move is not a new attempt


def test_a_send_past_the_workers_limit_waits_for_a_later_tick(drive):
    drive.configure("  goal_limits: {workers: 1}\n")
    first, second = _submit(drive), _submit(drive, todo="acme/app#2")
    _end(drive, first, "done")
    _end(drive, second, "done")  # no live run, both without their deliverable
    _tick(drive)
    assert _requests(drive, first) == ["request.1", "request.2"] and _requests(drive, second) == ["request.1"]
    assert _ledger(drive)["notes"][second] == "deferred: the workers limit (1) is reached (1 live)"
    assert not _writes(drive, "block", GOAL) and "stopped" not in _ledger(drive)
    _tick(drive)
    assert _requests(drive, second) == ["request.1"]
    _end(drive, first, "done", report=True)  # the first one's run 2 ended and delivered
    _tick(drive)
    assert _requests(drive, second) == ["request.1", "request.2"]
    assert "workers" not in _ledger(drive)  # holds are never kept in the ledger


class HubStopped(BaseException):
    """The hub process ends here (no handler of the hub or the drive catches it)."""


def _stop_after(drive, verb: str) -> dict:
    """The hub stops right after ``Hub.<verb>`` returned, before the drive saves its ledger again."""
    state = {"armed": True, "fired": False}
    real, real_replace = getattr(Hub, verb), goal_drive._replace

    def call(self, *args, **kwargs):
        result = real(self, *args, **kwargs)
        state["fired"] = state["armed"]
        return result

    def replace(path, record):
        if state["fired"] and state["armed"]:
            raise HubStopped
        return real_replace(path, record)
    drive.mp.setattr(Hub, verb, call)
    drive.mp.setattr(goal_drive, "_replace", replace)
    return state


def test_a_hub_stopped_after_a_follow_up_holds_no_worker_and_counts_each_run_once(drive):
    drive.configure("  goal_limits: {workers: 1, runs: 3}\n")
    task = _submit(drive)
    _end(drive, task, "done")
    stop = _stop_after(drive, "send")
    with pytest.raises(HubStopped):
        _tick(drive)  # the follow-up started run 2; the ledger never heard of it
    stop["armed"] = False
    assert _requests(drive, task) == ["request.1", "request.2"]
    assert _aid(task, 1, "deliverable") not in _ledger(drive)["actions"]
    _end(drive, task, "done")  # run 2 ended, still without its deliverable
    _tick(drive)  # a new Hub on the same root
    assert _requests(drive, task) == ["request.1", "request.2", "request.3"]
    ledger = _ledger(drive)
    assert ledger["runs"] == {task: 3} and "stopped" not in ledger and task not in ledger["notes"]


def test_a_send_its_request_key_shows_went_through_is_recorded_from_the_key_and_not_sent_again(drive):
    """A pass that meets an action the hub sent before it stopped (its key record says so, the ledger does not): the
    pass here is built on the task as it was before the send, the only way the drive meets that action again."""
    task = _submit(drive)
    _end(drive, task, "done")
    before = _hub(drive).store.load(task)
    stop = _stop_after(drive, "send")
    with pytest.raises(HubStopped):
        _tick(drive)
    stop["armed"] = False
    hub = _hub(drive)
    card = {"card": before["card"], "view": kanban_link.kept_view(hub.store, before["card"]), "jobs": [before]}
    entry = {"board": BOARD, "kind": "hermes", "cards": {WORK: card}}
    with hub.store.lock("goal-t-goal"):
        goal_drive.Drive(hub, goal_drive.Board(hub.worker), GOAL, entry)._job(card, before)
    assert _requests(drive, task) == ["request.1", "request.2"]
    assert not [r for r in hub.store.receipts_of(task) if r["kind"] == "request_replayed"]
    ledger = _ledger(drive)
    assert ledger["actions"][_aid(task, 1, "deliverable")]["result"].startswith("recovered from its request key")
    assert ledger["followups"] == {task: 1}


def _two_runs_of_one_todo(drive) -> None:
    first = _submit(drive)
    _end(drive, first, "failed")
    _end(drive, _submit(drive, host="there"), "failed")


@pytest.mark.parametrize(("limit", "make", "words"), [
    ("workers", lambda d: (_submit(d), _submit(d, todo="acme/app#2"), _end(d, _submit(d, todo="acme/app#3", host="there"), "done")),
     "2 live runs"),
    ("attempts", _two_runs_of_one_todo, "2 attempts on one to-do"),
    ("runs", lambda d: _end(d, _submit(d), "done"), "the next run would be run 2"),
    ("wall_h", lambda d: _end(d, _hub(d).store.update(_submit(d), created=time.time() - 7200)["id"], "failed"),
     "h"),
])
def test_each_limit_blocks_the_goal_card_with_its_reason_and_stops_the_drive(drive, limit, make, words):
    drive.configure(f"  goal_limits: {{{limit}: 1}}\n")
    make(drive)
    _tick(drive)
    (block,) = _writes(drive, "block", GOAL)
    assert block[2:4] == ["--kind", "needs_input"]
    assert f"stopped at its {limit} limit (1): " in block[-1] and words in block[-1]
    assert _ledger(drive)["stopped"]["limit"] == limit
    before = len(_kanban(drive))
    _tick(drive)
    assert [call[0] for call in _kanban(drive)[before:]] == ["show"] * (len(_kanban(drive)) - before)
    _never_done(drive)


def test_two_follow_ups_in_one_tick_never_pass_the_runs_limit(drive):
    drive.configure("  goal_limits: {runs: 3}\n")
    first, second = _submit(drive), _submit(drive, todo="acme/app#2")
    _end(drive, first, "done")
    _end(drive, second, "done")  # 2 runs of 3
    _tick(drive)
    sent = [task for task in (first, second) if _requests(drive, task) == ["request.1", "request.2"]]
    assert len(sent) == 1
    (block,) = _writes(drive, "block", GOAL)
    assert "stopped at its runs limit (3): the next run would be run 4" in block[-1]
    assert sum(_ledger(drive)["runs"].values()) == 3


def test_a_goal_card_the_board_refuses_to_block_is_not_stopped_and_is_tried_again_next_tick(drive):
    drive.configure("  goal_limits: {runs: 1}\n")
    _set_status(drive, GOAL, "review")  # block takes running or ready only, and review has no step to ready
    _end(drive, _submit(drive), "done")
    _tick(drive)
    ledger = _ledger(drive)
    assert "stopped" not in ledger and ledger["notes"][GOAL].startswith("card_write_refused: block on a review card")
    assert len(_writes(drive, "block", GOAL)) == 1
    _tick(drive)
    assert len(_writes(drive, "block", GOAL)) == 2 and "stopped" not in _ledger(drive)
    _set_status(drive, GOAL, "ready")
    _tick(drive)
    assert _ledger(drive)["stopped"]["limit"] == "runs"
    assert not any(_requests(drive, job["id"])[1:] for job in _hub(drive).store.all())


@pytest.fixture
def lina_board(drive, tmp_path, monkeypatch):
    """The drive on LINA's own board (``card_link: {kind: lina}``): a scratch kanban database."""
    from pathlib import Path

    from hermes_cli import kanban_db as kb
    from hermes_cli import kanban_db_connect as kbc
    home = tmp_path / "hermes_home"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for var in ("HERMES_KANBAN_DB", "HERMES_KANBAN_WORKSPACES_ROOT", "HERMES_KANBAN_HOME", "HERMES_KANBAN_BOARD"):
        monkeypatch.delenv(var, raising=False)
    kb._INITIALIZED_PATHS.clear()
    drive.configure("  goal_limits: {runs: 1}\n", hermes=False)

    def cards(goal_status: str) -> tuple[str, str]:
        with kbc.connect_closing() as conn:
            goal = kb.create_task(conn, title="Ship the thing")
            work = kb.create_task(conn, title="Build it", body=f"## Goal\n\n{goal} — Ship the thing\n")
            if goal_status == "scheduled":
                assert kb.schedule_task(conn, goal, reason="the goal session waits on its cards")
            else:
                kb.link_tasks(conn, work, goal)  # the plan's layout: the goal waits in todo on its work card
            assert kb.get_task(conn, goal).status == goal_status
        return goal, work
    return SimpleNamespace(cards=cards, kb=kb, kbc=kbc)


def _lina_run(drive, work: str) -> str:
    task = _submit(drive, card={"task": work, "board": None})
    _end(drive, task, "done")  # no deliverable: a follow-up would start run 2 of a budget of 1
    return task


def test_a_scheduled_goal_card_on_a_lina_board_is_blocked_when_a_limit_is_reached(drive, lina_board):
    goal, work = lina_board.cards("scheduled")
    task = _lina_run(drive, work)
    _tick(drive)
    with lina_board.kbc.connect_closing() as conn:
        assert lina_board.kb.get_task(conn, goal).status == "blocked"
        (blocked,) = [e for e in lina_board.kb.list_events(conn, goal) if e.kind == "blocked"]
    assert blocked.payload["kind"] == "needs_input" and "runs limit (1)" in blocked.payload["reason"]
    assert _ledger_of(drive, goal)["stopped"]["limit"] == "runs"
    assert _requests(drive, task) == ["request.1"]


def test_a_goal_card_in_todo_on_a_lina_board_is_not_stopped_while_the_board_refuses_its_block(drive, lina_board):
    goal, work = lina_board.cards("todo")
    task = _lina_run(drive, work)
    _tick(drive)
    ledger = _ledger_of(drive, goal)
    assert "stopped" not in ledger
    assert ledger["notes"][goal].startswith("card_write_refused: block on a todo card: promote refused")
    _tick(drive)
    assert "stopped" not in _ledger_of(drive, goal) and _requests(drive, task) == ["request.1"]


def _ledger_of(drive, goal: str) -> dict:
    return json.loads((drive.root / "hub" / "goals" / f"{goal.replace('_', '-')}.json").read_text())


def test_a_failed_run_is_not_read_as_done_and_the_drive_never_completes_a_card(drive):
    task = _submit(drive)
    _end(drive, task, "failed", report=True)
    _tick(drive)
    _tick(drive)
    assert _ledger(drive)["actions"] == {}
    assert {call[0] for call in _kanban(drive)} <= {"show", "comment"}
    _never_done(drive)


def test_the_drive_is_off_with_worker_goal_drive_false(drive):
    drive.configure("  goal_drive: false\n")
    task = _submit(drive)
    _end(drive, task, "done")
    _tick(drive)
    assert _requests(drive, task) == ["request.1"]
    assert not (drive.root / "hub" / "goals").exists()


def test_goal_limits_goal_drive_and_usage_limit_are_validated_work_settings():
    assert ws.validate({"worker": {"goal_drive": False, "goal_limits": {"workers": 2, "usage_move": True},
                                   "runners": {"x": {"type": "omo", "usage_limit": "(?i)quota"}}}}) == []
    paths = {e["path"] for e in ws.validate({"worker": {"goal_drive": "no", "goal_limits": {"runs": 0, "turns": 3},
                                                        "runners": {"x": {"type": "omo", "usage_limit": "("}}}})}
    assert paths == {"worker.goal_drive", "worker.goal_limits.runs", "worker.goal_limits.turns",
                     "worker.runners.x.usage_limit"}


def test_action_ids_are_request_keys_and_a_reset_time_is_read_from_the_end_text():
    assert goal_drive.action_id("t_goal", "t_work1", "OT-ABC1107000000", 3, "red-check") == \
        "t-goal:t-work1:OT-ABC1107000000:3:red-check"
    assert goal_drive.reset_at("limit resets at 2030-01-01T00:00:00Z", 0) == 1893456000
    assert goal_drive.reset_at("try again in 2 hours", 100) == 100 + 7200
    assert goal_drive.reset_at("rate limit", 100) == 100 + goal_drive.USAGE_WAIT_S


def _judge_wakes(drive) -> list[list[str]]:
    return [call for call in _writes(drive, "unblock", WORK) if "--judge-wake" in call]


def _later(drive, seconds: float) -> None:
    real = time.time
    drive.mp.setattr(goal_drive.time, "time", lambda: real() + seconds)


def test_a_hermes_without_wait_for_gets_a_plain_park_and_the_hub_wakes_it_once_the_checks_settle(drive):
    """lina#419: the installed Hermes has no ``schedule --wait-for``, so the hub parks with the wait in the reason and
    wakes the card itself."""
    drive.mp.setenv("FAKE_NO_WAIT_FOR", "1")
    _red_check(drive, conclusion=None, status="in_progress")
    task = _submit(drive, repo="acme/app")
    _end(drive, task, "done", report=True)
    _tick(drive)
    probe, park = _writes(drive, "schedule", WORK)
    assert probe[2:4] == ["--wait-for", "pr:acme/app#7"]
    assert park[2:] == ["--", "goal drive: waiting on the required checks of #7 · WAIT_EVENT=pr:acme/app#7 "
                              f"GOAL_ACTION={_aid(task, 1, 'check-park')}"]
    assert "card_write_refused" not in json.dumps(_ledger(drive)["notes"])
    _red_check(drive, conclusion="success")
    _tick(drive)  # the pending read is kept for 60 s
    assert not _judge_wakes(drive)
    _later(drive, goal_drive.CHECK_CACHE_S + 1)
    _tick(drive)
    _tick(drive)
    assert _judge_wakes(drive) == [["unblock", WORK, "--judge-wake", task, "--reason",
                                    "goal drive: wait met: required checks of acme/app#7 finished"]]
    assert json.loads((drive.cards / f"{WORK}.json").read_text())["status"] == "ready"
    assert _ledger(drive)["waits"] == {} and _aid(task, 1, "check-wake") in _ledger(drive)["actions"]
    assert _requests(drive, task) == ["request.1"]


def test_a_hermes_without_wait_for_gets_a_plain_park_and_the_hub_wakes_it_once_the_time_passed(drive):
    drive.mp.setenv("FAKE_NO_WAIT_FOR", "1")
    task = _submit(drive)
    _usage_end(drive, task)
    _tick(drive)
    park = _writes(drive, "schedule", WORK)[-1]
    assert park[2] == "--" and " · WAIT_EVENT=until:" in park[3] and park[3].endswith(
        f"GOAL_ACTION={_aid(task, 1, 'usage-park')}")
    _tick(drive)
    assert not _judge_wakes(drive)
    _later(drive, 1801)
    _tick(drive)
    _tick(drive)
    (wake,) = _judge_wakes(drive)
    assert wake[:5] == ["unblock", WORK, "--judge-wake", task, "--reason"]
    assert wake[5].startswith("goal drive: wait met: time ") and wake[5].endswith(" passed")
    assert _requests(drive, task) == ["request.1", "request.2"]  # and the run goes on after its usage limit


def test_a_card_parked_again_by_someone_else_is_not_woken_by_the_hub(drive):
    drive.mp.setenv("FAKE_NO_WAIT_FOR", "1")
    task = _submit(drive)
    _usage_end(drive, task)
    _tick(drive)
    path = drive.cards / f"{WORK}.json"
    card = json.loads(path.read_text())
    card["events"].append({"kind": "scheduled", "payload": {"reason": "waiting for the owner's call"}})
    path.write_text(json.dumps(card))
    _later(drive, 1801)
    _tick(drive)
    _tick(drive)
    assert not _judge_wakes(drive) and _ledger(drive)["waits"] == {}
    assert json.loads(path.read_text())["status"] == "scheduled"
