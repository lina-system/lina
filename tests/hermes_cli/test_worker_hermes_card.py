"""``lina worker`` runs handed out from a Hermes kanban card (lina#227).

With ``worker.card_link: {kind: hermes, command: [...]}`` every write to the card goes through that command
(``kanban comment | attach | show | unblock``); LINA code never opens a kanban database. The commands here are
a fake that records its argv, except where the real kanban CLI runs against a scratch Hermes home."""

import json
import sqlite3
import sys
import textwrap
import time
from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.worker import hub as hub_module
from hermes_cli.worker import events, kanban_link, serve, settings
from hermes_cli.worker.board import cards
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.transport import TransportError
from tests.hermes_cli import _worker_fakes as fakes

AGENT = "agent-a"
CARD, BOARD = "t_card1", "ops"

FAKE_KANBAN = """import json, os, sys
JUDGE_WAKE = True  # the owner's Hermes: unblock takes --judge-wake <run>
verb = sys.argv[sys.argv.index("kanban") + 3] if "--board" in sys.argv else sys.argv[sys.argv.index("kanban") + 1]
rest = sys.argv[sys.argv.index(verb) + 1:]
if verb == "unblock" and not (len(rest) == 1 or (JUDGE_WAKE and len(rest) == 3 and rest[0] == "--judge-wake")):
    with open(os.environ["FAKE_KANBAN_LOG"], "a") as fh:
        fh.write(json.dumps({"argv": sys.argv[1:], "ok": False, "content": None, "env": []}) + "\\n")
    print("usage: hermes kanban unblock [-h] task_id", file=sys.stderr)
    print("hermes: error: unrecognized arguments: " + " ".join(rest[:-1]), file=sys.stderr)
    sys.exit(2)
once = os.path.join(os.environ.get("FAKE_KANBAN_FAIL_ONCE", ""), verb)
failed = bool(os.environ.get("FAKE_KANBAN_FAIL_ONCE")) and os.path.exists(once)
if failed:
    os.remove(once)
if "HERMES_DELEGATED_CHILD_CONTEXT" in os.environ and verb != "show":
    failed = True
    print("delegate_task child contexts cannot mutate", file=sys.stderr)
# Hermes keeps a copy of an attached file: record what it was given at that moment.
content = open(sys.argv[sys.argv.index(verb) + 2], encoding="utf-8").read() if verb == "attach" and not failed else None
with open(os.environ["FAKE_KANBAN_LOG"], "a") as fh:
    fh.write(json.dumps({"argv": sys.argv[1:], "ok": not failed, "content": content,
                         "env": sorted(k for k in os.environ if k.startswith("HERMES_"))}) + "\\n")
if verb == "show":
    print(json.dumps({"task": {"status": os.environ.get("FAKE_KANBAN_STATUS", "scheduled")}}))
sys.exit(1 if failed else 0)
"""
FAKE_KANBAN_NO_JUDGE_WAKE = FAKE_KANBAN.replace("JUDGE_WAKE = True", "JUDGE_WAKE = False")  # upstream Hermes
FAKE_CHAT = """import os, sys
open(os.environ["FAKE_CHAT_LOG"], "a").write(sys.stdin.read())
"""


def _config(base: Path, command: list[str], runner: list[str] | None = None, devices: str = "", extra: str = "") -> None:
    base.mkdir(parents=True, exist_ok=True)
    runners = f"    fakecli: {json.dumps({'type': 'command', 'argv': runner, 'access': 'workspace-write'})}\n" if runner else "    {}\n"
    (base / "config.yaml").write_text(
        "devices:\n  workers:\n" + textwrap.indent(devices or "{}", "    ") + "\n"
        "worker:\n  poll_s: 0.05\n  stall_grace_s: 30\n  answered_grace_s: 0.2\n  min_free_gb: 0\n  min_free_mem_gb: 0\n"
        f"  card_link: {json.dumps({'kind': 'hermes', 'command': command})}\n" + extra + "  runners:\n" + runners,
        encoding="utf-8")


@pytest.fixture
def fake_kanban(tmp_path, monkeypatch):
    script = tmp_path / "fake_kanban.py"
    script.write_text(FAKE_KANBAN, encoding="utf-8")
    log = tmp_path / "kanban.jsonl"
    monkeypatch.setenv("FAKE_KANBAN_LOG", str(log))

    def calls() -> list[dict]:
        return [json.loads(line) for line in log.read_text(encoding="utf-8").splitlines()] if log.is_file() else []
    return [sys.executable, str(script)], calls


def _verbs(calls: list[dict]) -> list[str]:
    """The verbs of the calls that went through."""
    return [call["argv"][call["argv"].index(BOARD) + 1] for call in calls if call["ok"]]


def _no_kanban_db(monkeypatch) -> list[str]:
    """Record (and refuse) any kanban database this process opens."""
    opened: list[str] = []
    real = sqlite3.connect

    def guarded(database, *args, **kwargs):
        if "kanban" in str(database):
            opened.append(str(database))
            raise AssertionError(f"LINA opened a kanban database: {database}")
        return real(database, *args, **kwargs)

    def refused(*args, **kwargs):
        opened.append("kanban_db_connect.connect")
        raise AssertionError("LINA opened a kanban database through kanban_db_connect")
    monkeypatch.setattr(sqlite3, "connect", guarded)
    monkeypatch.setattr(kbc, "connect", refused)
    return opened


def _card_session(monkeypatch, card: str = CARD, board: str = BOARD) -> None:
    monkeypatch.setenv("HERMES_KANBAN_TASK", card)
    monkeypatch.setenv("HERMES_KANBAN_BOARD", board)


def _one_run_on_a_device(tmp_path, monkeypatch, command) -> tuple[Path, str]:
    base, fake = tmp_path / "lina", tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    chat = tmp_path / "chat.py"
    chat.write_text(FAKE_CHAT, encoding="utf-8")
    monkeypatch.setenv("FAKE_CHAT_LOG", str(tmp_path / "chat.log"))
    _config(base, command, argv, devices="here: {host: local, cap: 2}",
            extra=f"  notice: {json.dumps({'command': [sys.executable, str(chat)]})}\n")
    _card_session(monkeypatch)
    root = base / "worker"
    launcher = fakes.ThreadLauncher().install(monkeypatch)
    serve.serve_once(root)
    lines: list[str] = []
    Hub(root, out=lines.append).submit({"briefs": [fakes.brief(tmp_path, "SCENARIO:done")], "mode": "ulw",
                                        "bot": AGENT, "runner": "fakecli", "wait": None})
    task_id = lines[0].split()[0]
    serve.serve_once(root)
    hub = Hub(root, out=lines.append)
    fakes.until(lambda: "done:1" in fakes.notified(hub.store.load(task_id)), what="the run ended")
    launcher.join()
    return root, task_id


def test_a_hermes_card_gets_start_end_and_report_through_the_command_and_no_kanban_db_is_opened(
        tmp_path, monkeypatch, fake_kanban):
    command, calls = fake_kanban
    opened = _no_kanban_db(monkeypatch)
    _, task_id = _one_run_on_a_device(tmp_path, monkeypatch, command)

    assert opened == []
    made = calls()
    assert _verbs(made) == ["comment", "comment", "attach", "show"]  # show: does the card's schedule name the run
    start, end, attach, _ = (call["argv"] for call in made)
    assert start[:5] == ["kanban", "--board", BOARD, "comment", CARD] and task_id in start[-1]
    assert end[:5] == ["kanban", "--board", BOARD, "comment", CARD]
    assert end[end.index("--author") + 1] == "lina-worker"
    assert f"lina worker accept {task_id}" in end[-1]
    assert attach[:5] == ["kanban", "--board", BOARD, "attach", CARD]
    assert Path(attach[5]).name == "report.md" and "## 다음에 알아야 할 것" in made[2]["content"]
    # unblock refuses inside a card session's environment, and LINA's home must not leak into Hermes's CLI
    assert not [name for call in made for name in call["env"]
                if name.startswith("HERMES_KANBAN_") or name in ("HERMES_HOME", "HERMES_PROFILE")]


def test_a_run_on_a_hermes_card_sends_no_chat_notice(tmp_path, monkeypatch, fake_kanban):
    command, _ = fake_kanban
    root, task_id = _one_run_on_a_device(tmp_path, monkeypatch, command)
    assert not (tmp_path / "chat.log").exists()
    notified = [r for r in Hub(root, out=lambda *_: None).store.receipts_of(task_id) if r["kind"] == "notified"]
    assert len(notified) == 1 and "card" in notified[0]["how"]


@pytest.fixture
def queued(tmp_path, fake_kanban):
    """A hub with no devices: runs stay queued, and their ends are reported by hand."""
    command, calls = fake_kanban
    base = tmp_path / "lina"
    _config(base, command)
    hub = Hub(base / "worker", out=lambda *_: None)

    def submit(name: str = "brief.md") -> str:
        lines: list[str] = []
        Hub(hub.worker_root, out=lines.append).submit({
            "briefs": [fakes.brief(tmp_path, "do it", name)], "mode": "ulw", "bot": AGENT, "topic": "t",
            "wait": None, "card": {"task": CARD, "board": BOARD}})
        hub.tick()
        return lines[0].split()[0]
    return hub, submit, calls


def test_wait_parks_the_card_and_the_first_end_wakes_it_once(queued):
    hub, submit, calls = queued
    first, second = submit("a.md"), submit("b.md")
    assert hub.wait({"task": CARD, "board": BOARD}) == 0
    assert _verbs(calls()) == ["comment", "comment"]

    hub.notify(first, "done", "1")
    hub.notify(first, "done", "1")  # the same end reported again (device and tick) adds nothing
    assert _verbs(calls())[2:] == ["comment", "show", "unblock"]
    assert calls()[-1]["argv"][-1] == CARD

    hub.notify(second, "done", "1")  # the card runs again: the comment only
    assert _verbs(calls())[5:] == ["comment", "show"]


def test_wait_on_one_run_is_woken_only_by_that_runs_end(queued):
    hub, submit, calls = queued
    waited, other = submit("a.md"), submit("b.md")
    assert hub.wait({"task": CARD, "board": BOARD}, [waited]) == 0
    hub.notify(other, "done", "1")
    assert _verbs(calls())[2:] == ["comment", "show"]
    hub.notify(waited, "done", "1")
    hub.notify(waited, "done", "1")
    assert _verbs(calls())[2:] == ["comment", "show", "comment", "show", "unblock"]


def test_a_card_write_that_fails_once_is_retried_without_repeating_what_went_through(queued, tmp_path, monkeypatch):
    hub, submit, calls = queued
    run = submit()
    assert hub.wait({"task": CARD, "board": BOARD}) == 0
    report = hub.store.out_dir(hub.store.load(run)) / "report.md"
    report.parent.mkdir(parents=True)
    report.write_text("# report\n", encoding="utf-8")
    fail = tmp_path / "fail-once"
    fail.mkdir()
    for verb in ("comment", "attach", "unblock"):
        (fail / verb).touch()
    monkeypatch.setenv("FAKE_KANBAN_FAIL_ONCE", str(fail))

    hub.notify(run, "done", "1")  # the comment fails
    hub.notify(run, "done", "1")  # the device reports again: the comment goes, the attachment fails
    hub.tick()  # the attachment goes, the unblock fails
    hub.tick()  # the unblock goes
    hub.tick()
    hub.notify(run, "done", "1")
    assert _verbs(calls()) == ["comment", "comment", "attach", "show", "show", "unblock"]
    assert not hub.store.load(run).get("card_pending")


class HubStopped(BaseException):
    """The hub process ending where it is raised."""


def test_a_hub_that_stops_once_a_card_end_is_staged_does_each_card_step_once(queued, monkeypatch):
    hub, submit, calls = queued
    run = submit()
    assert hub.wait({"task": CARD, "board": BOARD}) == 0
    report = hub.store.out_dir(hub.store.load(run)) / "report.md"
    report.parent.mkdir(parents=True)
    report.write_text("# report\n", encoding="utf-8")
    record = events.record

    def stop(*args, **kwargs):  # the end is staged; marking it done is where the hub stops
        monkeypatch.setattr(events, "record", record)
        raise HubStopped
    monkeypatch.setattr(events, "record", stop)
    with pytest.raises(HubStopped):
        hub.notify(run, "done", "1")

    Hub(hub.worker_root, out=lambda *_: None).tick()  # the restarted hub
    hub.notify(run, "done", "1")  # the device's report arrives again
    Hub(hub.worker_root, out=lambda *_: None).tick()

    assert _verbs(calls()) == ["comment", "comment", "attach", "show", "unblock"]
    job = hub.store.load(run)
    assert not job.get("card_pending") and not job.get("deliveries_pending")
    assert [r["delivery"] for r in hub.store.receipts_of(run) if r["kind"] == "notified"] == [f"{run}:1:run.completed"]


def _attached(calls: list[dict]) -> list[tuple[str, str]]:
    """``(stored name, content)`` of each attachment that went through."""
    out = []
    for call in calls:
        argv = call["argv"]
        if call["ok"] and argv[argv.index(BOARD) + 1] == "attach":
            out.append((argv[argv.index("--name") + 1], call["content"]))
    return out


@pytest.fixture
def on_a_device(queued, monkeypatch):
    """A run placed on a device whose result fetch is played by *results*: each call returns the next entry, an
    exception is raised, a string is written as the run's report.md, ``(report, run)`` also writes a manifest of
    that run, None writes no report."""
    hub, submit, calls = queued
    run = submit()
    hub.store.update(run, host="far", state="running")
    results: list = []

    def fetch(transport, task_id, out_dir):
        outcome = results.pop(0) if results else None
        if isinstance(outcome, Exception):
            raise outcome
        out_dir.mkdir(parents=True, exist_ok=True)
        if isinstance(outcome, tuple):
            outcome, manifest_run = outcome
            (out_dir / "manifest.json").write_text(json.dumps({"run": manifest_run, "files": {}}), encoding="utf-8")
        if outcome is not None:
            (out_dir / "report.md").write_text(outcome, encoding="utf-8")
    monkeypatch.setattr(hub_module, "fetch", fetch)
    monkeypatch.setattr(Hub, "transport", lambda self, job: None)
    monkeypatch.setattr(Hub, "meta", lambda self, job, live=False: {"state": "running", "alive": True})
    assert hub.wait({"task": CARD, "board": BOARD}) == 0
    return hub, run, results, calls


def test_a_failed_result_fetch_is_retried_before_the_report_is_attached_and_the_card_woken(on_a_device):
    hub, run, results, calls = on_a_device
    results += [TransportError("far: connection reset"), "# run 1 report\n"]
    hub.notify(run, "done", "1")
    assert _verbs(calls())[1:] == []  # the end waits for its result
    hub.tick()
    assert _verbs(calls())[1:] == ["comment", "attach", "show", "unblock"]
    assert _attached(calls()) == [(f"report-{run}-1.md", "# run 1 report\n")]
    assert not hub.store.load(run).get("card_pending")


def test_a_retried_fetch_that_finds_a_later_run_does_not_label_its_results_as_the_earlier_run(on_a_device):
    hub, run, results, calls = on_a_device
    results += [TransportError("far: connection reset"), ("run 2 report\n", 2), ("run 2 report\n", 2)]
    hub.notify(run, "done", "1")
    hub.store.update(run, runs=2, state="running")  # `send` started run 2 on the device before the retry
    hub.tick()
    assert _verbs(calls())[1:] == ["comment", "show", "unblock"]
    assert "Run 1's results were replaced by a later run before they could be fetched" in calls()[1]["argv"][-1]
    hub.notify(run, "done", "2")
    assert _verbs(calls())[1:] == ["comment", "show", "unblock", "comment", "attach", "show"]
    assert _attached(calls()) == [(f"report-{run}-2.md", "run 2 report\n")]
    assert not hub.store.load(run).get("card_pending")


def test_a_held_end_resumed_after_the_next_run_started_does_not_attach_that_runs_report(on_a_device):
    hub, run, results, calls = on_a_device
    results += [OSError("no space left on device"), ("run 2 report\n", 2)]
    with pytest.raises(OSError):
        hub.notify(run, "done", "1")  # run 1's notice could not be built: its end is held
    hub.store.update(run, runs=2, state="running")  # `send` started run 2, whose results the device holds now
    hub.tick()
    assert _verbs(calls())[1:] == ["comment", "show", "unblock"]
    assert _attached(calls()) == []
    assert "Run 1's results were replaced by a later run before they could be fetched" in calls()[1]["argv"][-1]


def test_a_run_with_no_report_completes_without_an_attachment(on_a_device):
    hub, run, results, calls = on_a_device
    results.append(None)
    hub.notify(run, "done", "1")
    assert _verbs(calls())[1:] == ["comment", "show", "unblock"]
    assert not hub.store.load(run).get("card_pending")


def test_a_result_that_never_arrives_ends_the_card_saying_so(on_a_device):
    hub, run, results, calls = on_a_device
    results += [TransportError("far: unreachable")] * hub_module.FETCH_TRIES
    hub.notify(run, "done", "1")
    for _ in range(hub_module.FETCH_TRIES):
        hub.tick()
    assert _verbs(calls())[1:] == ["comment", "show", "unblock"]
    end = calls()[1]["argv"][-1]
    assert "report.md could not be fetched" in end and f"lina worker result {run}" in end
    assert not hub.store.load(run).get("card_pending")


def test_a_retried_attachment_carries_its_own_runs_report(queued, tmp_path, monkeypatch):
    hub, submit, calls = queued
    run = submit()
    out = hub.store.out_dir(hub.store.load(run))
    out.mkdir(parents=True)
    (out / "report.md").write_text("run 1\n", encoding="utf-8")
    fail = tmp_path / "fail-once"
    fail.mkdir()
    (fail / "attach").touch()
    monkeypatch.setenv("FAKE_KANBAN_FAIL_ONCE", str(fail))
    hub.notify(run, "done", "1")
    (out / "report.md").write_text("run 2\n", encoding="utf-8")  # the next run's results land in the same folder
    hub.tick()
    assert _attached(calls()) == [(f"report-{run}-1.md", "run 1\n")]


def test_the_end_of_a_run_on_a_running_card_is_a_comment_only(queued):
    hub, submit, calls = queued
    run = submit()
    hub.notify(run, "failed", "1")
    assert _verbs(calls()) == ["comment", "comment", "show"]  # read only: no schedule names the run


def test_wait_with_no_open_run_on_the_card_does_not_park_it(queued):
    hub, submit, calls = queued
    run = submit()
    hub.notify(run, "done", "1")
    with pytest.raises(HubError, match="no open run"):
        hub.wait({"task": CARD, "board": BOARD})
    assert "schedule" not in _verbs(calls())


def test_hermes_card_runs_show_in_the_board_run_list_with_card_and_board(queued):
    hub, submit, _ = queued
    run = submit()
    lina_card = dict(hub.store.load(run), id="OT-AGE0000000001", card={"task": "t_lina", "board": None})
    hub.store.save(lina_card)  # a run on LINA's own board: #112 serves it as a work card
    shown = {row["id"]: row for row in cards(hub.worker_root, time.time())}
    assert set(shown) == {run}
    assert (shown[run]["card"], shown[run]["board"]) == (f"hermes:{CARD}", BOARD)  # the id /work/cards serves (lina#630)


def _unblocks(calls: list[dict]) -> list[tuple[list[str], bool]]:
    """``(argv after unblock, went through)`` of each unblock call."""
    return [(call["argv"][call["argv"].index("unblock") + 1:], call["ok"]) for call in calls
            if call["argv"][call["argv"].index(BOARD) + 1] == "unblock"]


def _noticed(hub, run: str) -> dict:
    return [r for r in hub.store.receipts_of(run) if r["kind"] == "card_noticed"][-1]


def test_the_wake_unblocks_the_card_to_judge_the_run_and_records_that_form(queued):
    """lina#341: ``--judge-wake <run>`` lands the card ready to judge the run even behind an open parent."""
    hub, submit, calls = queued
    run = submit()
    assert hub.wait({"task": CARD, "board": BOARD}) == 0
    hub.notify(run, "done", "1")
    assert _unblocks(calls()) == [(["--judge-wake", run, CARD], True)]
    assert (_noticed(hub, run)["woke"], _noticed(hub, run)["unblock"]) == (True, "judge_wake")


def test_a_hermes_without_judge_wake_is_unblocked_bare_once(queued, fake_kanban):
    hub, submit, calls = queued
    Path(fake_kanban[0][1]).write_text(FAKE_KANBAN_NO_JUDGE_WAKE, encoding="utf-8")
    run = submit()
    assert hub.wait({"task": CARD, "board": BOARD}) == 0
    hub.notify(run, "done", "1")
    assert _unblocks(calls()) == [(["--judge-wake", run, CARD], False), ([CARD], True)]
    assert (_noticed(hub, run)["woke"], _noticed(hub, run)["unblock"]) == (True, "bare")
    assert not hub.store.load(run).get("card_pending")


def test_an_unblock_refused_for_another_reason_is_not_retried_bare(queued, monkeypatch):
    hub, submit, calls = queued
    run = submit()
    monkeypatch.setenv("HERMES_DELEGATED_CHILD_CONTEXT", "1")
    with pytest.raises(kanban_link.CardLinkError, match="exited 1"):
        kanban_link.unblock(hub._link()["command"], hub.store.load(run)["card"], run)
    assert _unblocks(calls()) == [(["--judge-wake", run, CARD], False)]


def test_a_batch_run_wakes_the_card_to_judge_the_batch(queued, tmp_path):
    hub, _, calls = queued
    lines: list[str] = []
    Hub(hub.worker_root, out=lines.append).submit({
        "briefs": [fakes.brief(tmp_path, "a", "a.md"), fakes.brief(tmp_path, "b", "b.md")], "mode": "ulw",
        "bot": AGENT, "topic": "t", "wait": None, "card": {"task": CARD, "board": BOARD}})
    hub.tick()
    first = lines[0].split()[0]
    group = hub.store.load(first)["group"]
    assert group.startswith("OG-")
    assert hub.wait({"task": CARD, "board": BOARD}) == 0
    hub.notify(first, "done", "1")
    assert _unblocks(calls()) == [(["--judge-wake", group, CARD], True)]


def test_a_run_on_a_hermes_card_records_its_cards_and_an_older_record_gains_them_on_save(queued, tmp_path):
    """Hermes reads ``cards`` from the job record to hold the card until the run it launched is judged."""
    hub, submit, _ = queued
    run = submit()
    job = hub.store.load(run)
    assert job["cards"] == [{"id": CARD, "board": BOARD}]
    assert job["card"] == {"task": CARD, "board": BOARD, "kind": "hermes"}

    old = {key: value for key, value in job.items() if key != "cards"}
    old["card"] = {"task": CARD, "board": None, "kind": "hermes"}
    hub.store.path(run).write_text(json.dumps(old), encoding="utf-8")
    assert "cards" not in hub.store.load(run)
    hub.store.update(run, extra="touched")
    assert hub.store.load(run)["cards"] == [{"id": CARD, "board": "default"}]
    assert hub.store.load(run)["card"] == old["card"]


@pytest.fixture
def private(tmp_path, fake_kanban):
    command, _ = fake_kanban
    base, owned = tmp_path / "lina", tmp_path / "owner"
    owned.mkdir()
    (owned / "notes.md").write_text("the owner's own notes\n", encoding="utf-8")
    _config(base, command, fakes.install_fake_cli(tmp_path / "fake"),
            devices="here: {host: local, cap: 1}\nfar: {host: build-a, cap: 1}",
            extra=f"  private_paths: {json.dumps([str(owned)])}\n")
    lines: list[str] = []

    def submit(**opts) -> dict:
        Hub(base / "worker", out=lines.append).submit({
            "briefs": [fakes.brief(tmp_path, "read the notes")], "mode": "ulw", "bot": AGENT, "topic": "t",
            "wait": None, "runner": "fakecli", "inputs": [str(owned / "notes.md")], **opts})
        return Hub(base / "worker").store.load(next(line for line in lines if line.startswith("OT-")).split()[0])
    return submit, owned / "notes.md", base / "worker"


def test_private_files_are_refused_on_any_node_but_the_hub(private):
    submit, _, _ = private
    with pytest.raises(HubError, match="hub node"):
        submit(host="far")


def test_work_on_private_files_runs_on_the_hub_node_and_reads_them_in_place(private):
    """Named work waits until its device is ready (lina#228): the hub node's ``lina worker serve`` is not up at
    submit, so the run is pinned to it and queued, never bid out; the next tick after the hub comes up places it."""
    submit, notes, root = private
    job = submit()
    assert (job["fixed_host"], job["host"], job["state"]) == ("here", None, "queued")
    serve.serve_once(root)
    Hub(root, out=lambda *_: None).tick()
    job = Hub(root).store.load(job["id"])
    assert (job["fixed_host"], job["host"], job["state"]) == ("here", "here", "running")
    assert job["inputs"] == [] and job["private_inputs"] == [str(notes)]
    assert str(notes) in job["prompt"]


def _cli(argv: list[str]) -> int:
    import argparse
    from hermes_cli.subcommands.worker import _cmd_worker, build_worker_parser
    parser = argparse.ArgumentParser()
    build_worker_parser(parser.add_subparsers(dest="command"))
    args = parser.parse_args(["worker", *argv])
    with pytest.raises(SystemExit) as ended:
        _cmd_worker(args)
    return ended.value.code


@pytest.fixture
def card_shell(tmp_path, monkeypatch, fake_kanban):
    """A Hermes card session's environment: HERMES_HOME is Hermes's, HERMES_KANBAN_* name its own card."""
    command, calls = fake_kanban
    lina, hermes = tmp_path / "lina", tmp_path / "hermes"
    hermes.mkdir()
    _config(lina, command)
    monkeypatch.setenv("LINA_HOME", str(lina))
    monkeypatch.setenv("HERMES_HOME", str(hermes))
    monkeypatch.setenv("HERMES_KANBAN_HOME", str(hermes))
    _card_session(monkeypatch, "t_env", "envboard")
    return lina, hermes, calls


def test_submit_card_and_board_name_the_card_and_lina_home_alone_holds_the_state(tmp_path, card_shell):
    lina, hermes, calls = card_shell
    brief = fakes.brief(tmp_path, "do it")
    assert _cli(["submit", "--brief", brief, "--topic", "t", "--card", CARD, "--board", BOARD]) == 0
    jobs = list((lina / "worker" / "hub" / "jobs").glob("OT-*.json"))
    assert len(jobs) == 1 and json.loads(jobs[0].read_text("utf-8"))["card"] == {
        "task": CARD, "board": BOARD, "kind": "hermes"}
    assert calls() == []
    Hub(lina / "worker").tick()
    assert calls()[0]["argv"][:5] == ["kanban", "--board", BOARD, "comment", CARD]
    assert not (hermes / "worker").exists()


def test_the_card_session_environment_names_the_card(tmp_path, card_shell):
    lina, _, calls = card_shell
    assert _cli(["submit", "--brief", fakes.brief(tmp_path, "do it"), "--topic", "t"]) == 0
    Hub(lina / "worker").tick()
    assert calls()[0]["argv"][:5] == ["kanban", "--board", "envboard", "comment", "t_env"]
    assert _cli(["wait"]) == 0
    assert len(calls()) == 1


def test_the_state_root_comes_from_lina_home_never_from_a_hermes_card_session(tmp_path, monkeypatch):
    import hermes_constants
    account = tmp_path / "account"
    monkeypatch.setattr(hermes_constants, "_account_home", lambda: account)
    monkeypatch.setattr(Path, "home", lambda: account)
    monkeypatch.setenv("HERMES_HOME", str(account / ".hermes" / "profiles" / AGENT))
    monkeypatch.setenv("HERMES_KANBAN_HOME", str(account / ".hermes"))
    monkeypatch.setenv("HERMES_KANBAN_DB", str(account / ".hermes" / "kanban.db"))
    monkeypatch.setenv("LINA_HOME", str(tmp_path / "lina"))
    assert settings.root() == tmp_path / "lina" / "worker"
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / "elsewhere"))
    assert settings.root() == tmp_path / "lina" / "worker"
    monkeypatch.delenv("LINA_HOME")
    monkeypatch.setenv("HERMES_HOME", str(account / ".hermes" / "profiles" / AGENT))
    assert settings.root() == account / ".lina" / "worker"


def test_the_run_end_comment_reaches_the_running_card_session_mid_run(tmp_path, monkeypatch):
    """The 11-06 case from the worker's side: the hub runs with the agent's profile in its environment, so a
    comment left to the CLI's default author would carry the agent's own name and the card session would skip
    it as its own. The run's end is written as ``lina-worker`` and arrives in the running turn."""
    from tools import kanban_tools
    hermes = tmp_path / "hermes"
    hermes.mkdir()
    repo = Path(__file__).resolve().parents[2]
    command = ["env", f"HERMES_HOME={hermes}", f"PYTHONPATH={repo}", sys.executable, "-m", "hermes_cli.main"]
    monkeypatch.setenv("HERMES_HOME", str(hermes))
    monkeypatch.delenv("HERMES_KANBAN_BOARD", raising=False)
    kb._INITIALIZED_PATHS.clear()
    with kbc.connect_closing() as conn:  # the test plays the Hermes side: the card and its session
        card = kb.create_task(conn, title="a card that hands out runs", assignee=AGENT)
    base = tmp_path / "lina"
    _config(base, command)
    monkeypatch.setenv("HERMES_PROFILE", AGENT)
    monkeypatch.setenv("HERMES_KANBAN_TASK", card)

    class Session:
        notes: list[str] = []

        def steer(self, note: str) -> bool:
            self.notes.append(note)
            return True

    session = Session()
    monkeypatch.setattr(kanban_tools, "_comment_watermark", {})
    monkeypatch.setattr(kanban_tools, "_comment_poll_last_attempt", 0.0)
    assert not kanban_tools.inject_new_comments_from_env(session)  # the first poll only marks what it has seen

    lines: list[str] = []
    with monkeypatch.context() as hub_side:
        opened = _no_kanban_db(hub_side)
        hub = Hub(base / "worker", out=lines.append)
        hub.submit({"briefs": [fakes.brief(tmp_path, "do it")], "mode": "ulw", "bot": AGENT, "topic": "t",
                    "wait": None, "card": {"task": card, "board": None}})
        run = lines[0].split()[0]
        hub.notify(run, "done", "1")
    assert opened == []

    monkeypatch.setattr(kanban_tools, "_comment_poll_last_attempt", 0.0)
    assert kanban_tools.inject_new_comments_from_env(session)
    assert "- lina-worker: " in session.notes[-1] and f"lina worker accept {run}" in session.notes[-1]
