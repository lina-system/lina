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
verb, task, rest = args[0], args[1], args[2:]
cards = os.environ["FAKE_CARDS"]
with open(os.path.join(cards, "calls.jsonl"), "a") as fh:
    fh.write(json.dumps([verb, task, *rest]) + "\\n")
path = os.path.join(cards, task + ".json")
card = json.load(open(path)) if os.path.exists(path) else {"title": task, "status": "running", "body": ""}
if verb == "show":
    print(json.dumps({"task": card, "events": []}))
elif verb in ("block", "schedule"):
    card["status"] = "blocked" if verb == "block" else "scheduled"
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

    def configure(extra: str = "", there: str = "{host: local, cap: 4}") -> None:
        base.mkdir(exist_ok=True)
        (base / "config.yaml").write_text(
            f"devices:\n  workers:\n    here: {{host: local, cap: 4}}\n    there: {there}\n"
            "worker:\n  poll_s: 0.05\n  stall_grace_s: 30\n  answered_grace_s: 0.2\n  min_free_gb: 0\n"
            "  unknown_after_s: 0\n"
            f"  card_link: {json.dumps({'kind': 'hermes', 'command': [sys.executable, str(script)]})}\n"
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
                "todo": todo, "bot": "sion", "runner": "fakecli", "wait": None, "host": host,
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
    assert list(_ledger(drive)["actions"]) == ["t-goal:t-work1:acme-app-1:1:1:deliverable"]
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
    assert [r["key"] for r in replayed] == ["t-goal:t-work1:acme-app-1:1:1:deliverable"]
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
    assert set(_ledger(drive)["actions"]) == {"t-goal:t-work1:acme-app-1:1:1:deliverable",
                                              "t-goal:t-work1:acme-app-2:1:1:deliverable"}


def test_an_action_id_too_long_for_a_request_key_writes_its_to_do_as_a_hash():
    import hashlib
    import re

    from hermes_cli.worker.request_keys import CALLER_KEY
    todo = "[ ] " + "a very long checklist to-do " * 8
    aid = goal_drive.action_id("t_goal", "t_work1", todo, 1, 2, "deliverable")
    assert re.fullmatch(CALLER_KEY, aid) and len(aid) <= 128
    assert aid == f"t-goal:t-work1:{hashlib.sha256(todo.encode()).hexdigest()[:12]}:1:2:deliverable"
    assert aid != goal_drive.action_id("t_goal", "t_work1", todo + "!", 1, 2, "deliverable")


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


def test_a_lost_node_waits_while_the_outage_is_open_and_moves_once_after_the_run_did_not_continue(drive):
    task = _submit(drive)
    _outage(drive, True)
    _tick(drive)
    _tick(drive)
    hub = _hub(drive)
    assert hub.store.load(task)["state"] == "unknown"
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
    assert moved["request_key"] == "t-goal:t-work1:acme-app-1:1:1:resubmit"


def test_a_lost_node_run_is_not_moved_to_a_node_that_takes_no_automatic_work(drive):
    drive.configure(there="{host: local, cap: 4, auto: false}")
    task = _submit(drive)
    _outage(drive, True)
    _tick(drive)
    _outage(drive, False)
    (drive.root / "tasks" / task / "rc.1").write_text("1\n")
    (drive.root / "tasks" / task / "state").write_text("failed\n")
    _tick(drive)
    assert [job["id"] for job in _hub(drive).store.all()] == [task]
    assert "there: only when named with --host" in _ledger(drive)["notes"][task]


def test_a_lost_node_whose_run_went_on_is_not_moved(drive):
    task = _submit(drive)
    _outage(drive, True)
    _tick(drive)
    _outage(drive, False)
    (drive.root / "tasks" / task / "state").write_text("running\n")
    _tick(drive)
    _tick(drive)
    assert [job["id"] for job in _hub(drive).store.all()] == [task]
    assert "t-goal:t-work1:acme-app-1:1:1:node-continued" in _ledger(drive)["actions"]


def test_a_lost_node_is_not_moved_while_another_run_of_the_same_to_do_is_live(drive):
    task = _submit(drive)
    _outage(drive, True)
    _tick(drive)
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


def _two_runs_of_one_todo(drive) -> None:
    first = _submit(drive)
    _end(drive, first, "failed")
    _end(drive, _submit(drive, host="there"), "failed")


@pytest.mark.parametrize(("limit", "make", "words"), [
    ("workers", lambda d: (_submit(d), _submit(d, todo="acme/app#2"), _end(d, _submit(d, todo="acme/app#3", host="there"), "done")),
     "2 live runs"),
    ("attempts", _two_runs_of_one_todo, "2 attempts on one to-do"),
    ("runs", lambda d: _end(d, _submit(d), "done"), "would start run 2"),
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
    assert goal_drive.action_id("t_goal", "t_work1", "acme/app#1", 2, 3, "red-check") == \
        "t-goal:t-work1:acme-app-1:2:3:red-check"
    assert goal_drive.reset_at("limit resets at 2030-01-01T00:00:00Z", 0) == 1893456000
    assert goal_drive.reset_at("try again in 2 hours", 100) == 100 + 7200
    assert goal_drive.reset_at("rate limit", 100) == 100 + goal_drive.USAGE_WAIT_S
