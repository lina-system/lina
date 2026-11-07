"""Run ends as three events, each delivered once (lina#223).

Every end the hub records is ``run.completed`` (with its end state), ``question`` or ``host.unavailable``: on the
receipts, the job, the card's event and comment and the chat notice, whose first line starts with it. Each has one
delivery id; a repeated report of the same end is recorded once as ``delivery_duplicate`` and sent nowhere. A node
that cannot be reached is told once per outage, after three failed contacts, and once when it is back; its runs go
back under watch then, so an end they reach afterwards is delivered once."""

import hashlib
import json
import sys
import time
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.worker import device, events, kanban_link, serve
from hermes_cli.worker import hub as hub_module
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.transport import Reply
from tests.hermes_cli import _worker_fakes as fakes

AGENT = "sion"
START = 1_800_000_000.0
TICK_S = 30
FAKE_CHAT = """import json, os, sys
text = sys.stdin.read()
once = os.environ.get("FAKE_CHAT_FAIL_ONCE", "")
if once and os.path.exists(once):
    os.remove(once)
    sys.exit("chat: the gateway is not running")
with open(os.environ["FAKE_CHAT_LOG"], "a", encoding="utf-8") as fh:
    fh.write(json.dumps({"agent": sys.argv[1], "text": text}) + "\\n")
"""


@pytest.fixture
def card(tmp_path, monkeypatch):
    """A card on a scratch board of LINA's own."""
    home = tmp_path / "hermes_home"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for var in ("HERMES_KANBAN_DB", "HERMES_KANBAN_WORKSPACES_ROOT", "HERMES_KANBAN_HOME", "HERMES_KANBAN_BOARD",
                "HERMES_KANBAN_TASK"):
        monkeypatch.delenv(var, raising=False)
    kb._INITIALIZED_PATHS.clear()
    conn = kbc.connect()
    task = kb.create_task(conn, title="work card")
    conn.close()
    return task


def _worker_root(tmp_path: Path, monkeypatch, runners: str = "{}\n") -> Path:
    chat = tmp_path / "chat.py"
    chat.write_text(FAKE_CHAT, encoding="utf-8")
    monkeypatch.setenv("FAKE_CHAT_LOG", str(tmp_path / "chat.jsonl"))
    base = tmp_path / "lina"
    fakes.write_config(base, runners,
                       extra=f"  notice: {json.dumps({'command': [sys.executable, str(chat), '{agent}']})}\n")
    return base / "worker"


def _chats(tmp_path: Path) -> list[str]:
    log = tmp_path / "chat.jsonl"
    return [json.loads(line)["text"] for line in log.read_text(encoding="utf-8").splitlines()] if log.is_file() else []


def _receipts(hub: Hub, task_id: str, kind: str) -> list[dict]:
    return [r for r in hub.store.receipts_of(task_id) if r["kind"] == kind]


def _on_card(task: str) -> tuple[list[str], list]:
    """The card's comments and its worker events after the run's start."""
    with kbc.connect_closing() as conn:
        return ([c.body for c in kb.list_comments(conn, task)],
                [e for e in kb.list_events(conn, task) if e.kind.startswith("worker_") and e.kind != "worker_run"])


def test_a_repeated_report_of_an_end_is_recorded_once_and_sent_once(card, tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_KANBAN_TASK", card)
    fake = tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    root = _worker_root(tmp_path, monkeypatch,
                        f"fakecli: {json.dumps({'type': 'command', 'argv': argv, 'access': 'workspace-write'})}\n")
    launcher = fakes.ThreadLauncher().install(monkeypatch)
    serve.serve_once(root)
    lines: list[str] = []
    hub = Hub(root, out=lines.append)
    hub.submit({"briefs": [fakes.brief(tmp_path, "SCENARIO:done")], "mode": "ulw", "bot": AGENT,
                "runner": "fakecli", "wait": None})
    task_id = lines[0].split()[0]
    unsaved = hub.store.load(task_id)  # the job as a hub that stops before it saves the end holds it

    serve.serve_once(root)
    fakes.until(lambda: _receipts(hub, task_id, "notified"), what="the device reported the end")
    launcher.join()
    device.report_end(root, device.task_of(root, task_id), "done", 1)  # the device retries its report
    hub.tick()  # the tick sees the ended run too
    hub.store.save(unsaved)
    Hub(root, out=lines.append).tick()  # a restarted hub finds the end again

    delivery = f"{task_id}:1:run.completed"
    assert len(_receipts(hub, task_id, "notified")) == 1
    assert len(_chats(tmp_path)) == 1
    comments, _ = _on_card(card)
    assert len(comments) == 1 and comments[0].splitlines()[-1] == f"delivery {delivery}"
    duplicates = _receipts(hub, task_id, "delivery_duplicate")
    assert len(duplicates) == 2 and {r["delivery"] for r in duplicates} == {delivery}
    assert hub.store.load(task_id)["state"] == "done"


class Far:
    """Runs on node ``far``, whose answers the test plays (``down``: every call is unreachable), on a clock that
    moves ``TICK_S`` after each tick."""

    def __init__(self, tmp_path: Path, monkeypatch, card: str):
        self.tmp, self.card = tmp_path, card
        self.root = _worker_root(tmp_path, monkeypatch)
        self.hub = Hub(self.root, out=lambda *_: None)
        self.down = False
        self.remote: dict[str, dict] = {}
        self.human: dict = {}
        self.now = START
        monkeypatch.setattr(time, "time", lambda: self.now)
        monkeypatch.setattr(Hub, "meta", lambda hub, job, live=False: self.meta(job))
        monkeypatch.setattr(Hub, "transport", lambda hub, job: SimpleNamespace(call=self.call))
        monkeypatch.setattr(hub_module, "fetch", lambda transport, task_id, out: out.mkdir(parents=True, exist_ok=True))

    def run(self, n: int, *, on_card: bool = False, **fields) -> str:
        task_id = f"OT-SIO100000000{n}"
        self.hub.store.save({"id": task_id, "bot": AGENT, "topic": "t", "mode": "ulw", "runner": "fakecli",
                             "host": "far", "state": "running", "runs": 1, "created": self.now,
                             "access": "workspace-write", "grant": {"access": "workspace-write"}, "notified": [],
                             "delivery": "pending", "card": {"task": self.card, "board": None} if on_card else None,
                             **fields})
        self.remote[task_id] = {"state": "running", "run": 1, "alive": True}
        return task_id

    def meta(self, job: dict) -> dict:
        if self.down:
            return {"state": "unknown", "unreachable": True, "why": "ssh: connect to host far: timed out"}
        return dict(self.remote[job["id"]])

    def call(self, argv, stdin=b"", timeout=90):
        return Reply(255, b"", "ssh: timed out") if self.down else Reply(0, json.dumps(self.human).encode(), "")

    def tick(self, times: int = 1) -> None:
        for _ in range(times):
            self.hub.tick()
            self.now += TICK_S

    def chats(self) -> list[str]:
        return _chats(self.tmp)

    def state(self, task_id: str) -> str:
        return self.hub.store.load(task_id)["state"]


@pytest.fixture
def far(card, tmp_path, monkeypatch):
    return Far(tmp_path, monkeypatch, card)


def _ended(far: Far, end: str) -> tuple[str, str]:
    task_id = far.run(1, on_card=True)
    far.hub.notify(task_id, end, "1")
    return task_id, f"{task_id}:1:run.completed"


def _cancelled(far: Far) -> tuple[str, str]:
    task_id = far.run(1, on_card=True, host=None, state="queued")
    far.hub.cancel(task_id, "not needed")
    return task_id, f"{task_id}:0:run.completed"


def _asked(far: Far) -> tuple[str, str]:
    task_id = far.run(1, on_card=True, human_since=START - 200)
    far.remote[task_id] = {"state": "running", "run": 1, "alive": True, "live": {"dash": "human"}}
    far.human = {"open": True, "run": 1, "question": "Which colour should ok.txt say?"}
    far.tick()
    key = hashlib.sha256(b"Which colour should ok.txt say?").hexdigest()[:12]
    return task_id, f"{task_id}:1:question:{key}"


def _node_down(far: Far) -> tuple[str, str]:
    task_id = far.run(1, on_card=True)
    far.down = True
    far.tick(3)
    return task_id, f"far:host.unavailable:{int(START)}"


PATHS = {
    "done": (lambda far: _ended(far, "done"), "run.completed", "done", "worker_run_ended"),
    "failed": (lambda far: _ended(far, "failed"), "run.completed", "failed", "worker_run_ended"),
    "cancelled": (_cancelled, "run.completed", "cancelled", "worker_run_ended"),
    "question": (_asked, "question", None, "worker_run_ended"),
    "node down": (_node_down, "host.unavailable", None, "worker_host_unavailable"),
}


@pytest.mark.parametrize("path", PATHS)
def test_each_end_carries_its_event_and_delivery_id_everywhere_it_is_recorded(far, path):
    drive, event, end_state, card_event = PATHS[path]
    task_id, delivery = drive(far)
    named = {"event": event, **({"end_state": end_state} if end_state else {})}

    for kind in ("notified",) if event == "question" else ("exec_end", "notified"):  # a question ends no run
        (receipt,) = _receipts(far.hub, task_id, kind)
        assert {k: receipt.get(k) for k in ("event", "end_state", "delivery")} == {"end_state": None, **named,
                                                                                    "delivery": delivery}
    (entry,) = far.hub.store.load(task_id)["notified"]
    assert entry == {"delivery": delivery, "run": entry["run"], **named}
    (chat,) = far.chats()
    headline = f"{event} ({end_state})" if end_state else event
    assert chat.startswith(f"{headline} · ") and chat.splitlines()[-1] == f"delivery {delivery}"
    comments, events = _on_card(far.card)
    assert len(comments) == 1 and comments[0].startswith(f"{headline} · ")
    assert comments[0].splitlines()[-1] == f"delivery {delivery}"
    assert [(e.kind, e.payload.get("event"), e.payload.get("end_state")) for e in events] == [
        (card_event, event, end_state)]


HEAD = "[lina worker · automatic] sion · topic t · run OT-SIO1000000001 (fakecli on {host}, run {run}, access " \
       "workspace-write): "
FIRST_LINES = {
    "done": "run.completed (done) · " + HEAD.format(host="far", run=1) + "done",
    "failed": "run.completed (failed) · " + HEAD.format(host="far", run=1) + "failed — looks like a problem of the task",
    "cancelled": "run.completed (cancelled) · " + HEAD.format(host="-", run=0) + "cancelled — not needed",
    "question": "question · " + HEAD.format(host="far", run=1) + "waiting for an answer — the agent asked a question",
    "node down": "host.unavailable · [lina worker · automatic] far cannot be reached since "
                 + time.strftime("%Y-%m-%d %H:%M", time.localtime(START))
                 + " (3 failed contacts in a row); its runs were not moved to another device:",
}


@pytest.mark.parametrize("path", PATHS)
def test_the_notice_of_each_kind_starts_with_its_event(far, path):
    PATHS[path][0](far)
    (chat,) = far.chats()
    assert chat.splitlines()[0] == FIRST_LINES[path]


def test_an_outage_over_many_ticks_is_told_once_naming_its_runs(far):
    first, second = far.run(1), far.run(2)
    far.down = True
    far.tick(2)
    assert far.chats() == []
    assert [far.state(first), far.state(second)] == ["running", "running"]

    far.tick(8)
    (chat,) = far.chats()
    assert f"- {first} · " in chat and f"- {second} · " in chat
    assert [far.state(first), far.state(second)] == ["unknown", "unknown"]
    assert [len(_receipts(far.hub, run, "notified")) for run in (first, second)] == [1, 1]


def test_a_node_back_is_one_line_and_a_later_outage_is_told_again(far):
    first, second = far.run(1), far.run(2)
    far.down = True
    far.tick(4)
    far.down = False
    far.tick(3)
    outage, back = far.chats()
    assert back == f"far is back after 2m; runs {first}, {second} continue\n"
    assert [far.state(first), far.state(second)] == ["running", "running"]

    far.down = True
    far.tick(5)
    assert len(far.chats()) == 3
    again = far.chats()[2]
    assert again.startswith("host.unavailable · ") and first in again and second in again
    assert again.splitlines()[-1] != outage.splitlines()[-1]  # a new outage: a new delivery id


@pytest.mark.parametrize("ends", ["after it is back", "while it is down"])
def test_a_run_that_outlives_an_outage_has_its_end_delivered_once(far, ends):
    task_id = far.run(1)
    far.down = True
    far.tick(3)
    assert far.state(task_id) == "unknown"
    if ends == "while it is down":
        far.remote[task_id] = {"state": "done", "run": 1, "alive": False}
    far.down = False
    far.tick()
    far.remote[task_id] = {"state": "done", "run": 1, "alive": False}
    far.tick(3)

    completed = [r for r in _receipts(far.hub, task_id, "notified") if r.get("event") == "run.completed"]
    assert [(r["end_state"], r["delivery"]) for r in completed] == [("done", f"{task_id}:1:run.completed")]
    assert far.state(task_id) == "done"
    outage, back, end = far.chats()
    assert outage.startswith("host.unavailable · ") and end.startswith("run.completed (done) · ")
    assert back == "far is back after 1m; " + (f"runs {task_id} continue\n" if ends == "after it is back"
                                               else "no run continues\n")


class HubStopped(BaseException):
    """The hub process ending where it is raised."""


def _fetch_failing_once(monkeypatch, error: BaseException) -> None:
    calls = []

    def fetch(transport, task_id, out):
        calls.append(task_id)
        if len(calls) == 1:
            raise error
        out.mkdir(parents=True, exist_ok=True)
    monkeypatch.setattr(hub_module, "fetch", fetch)


def _sent_once(far: Far, task_id: str) -> None:
    delivery = f"{task_id}:1:run.completed"
    (chat,) = far.chats()
    assert chat.startswith("run.completed (done) · ") and chat.splitlines()[-1] == f"delivery {delivery}"
    comments, _ = _on_card(far.card)
    assert len(comments) == 1 and comments[0].splitlines()[-1] == f"delivery {delivery}"
    assert [r["delivery"] for r in _receipts(far.hub, task_id, "notified")] == [delivery]
    job = far.hub.store.load(task_id)
    assert job["state"] == "done" and not job.get("deliveries_pending")


def test_an_end_whose_notice_could_not_be_built_goes_out_once_on_the_retry(far, monkeypatch):
    task_id = far.run(1, on_card=True)
    _fetch_failing_once(monkeypatch, OSError("no space left on device"))
    with pytest.raises(OSError):
        far.hub.notify(task_id, "done", "1")
    assert far.chats() == []

    far.hub.notify(task_id, "done", "1")  # the device retries its report
    far.hub.notify(task_id, "done", "1")
    far.tick()

    _sent_once(far, task_id)
    assert len(_receipts(far.hub, task_id, "delivery_duplicate")) == 1


def test_an_end_held_by_a_hub_that_stopped_before_sending_it_goes_out_once(far, monkeypatch):
    task_id = far.run(1, on_card=True)
    _fetch_failing_once(monkeypatch, HubStopped())
    with pytest.raises(HubStopped):
        far.hub.notify(task_id, "done", "1")
    assert far.chats() == []

    far.hub = Hub(far.root, out=lambda *_: None)
    far.tick()  # the restarted hub sends what was held
    far.hub.notify(task_id, "done", "1")  # the device's report arrives again

    _sent_once(far, task_id)
    assert len(_receipts(far.hub, task_id, "delivery_duplicate")) == 1


def test_an_outage_notice_a_card_did_not_take_is_retried_until_it_went_out(far, monkeypatch):
    task_id = far.run(1, on_card=True)
    record_host, tries = kanban_link.record_host, []

    def flaky(*args):
        tries.append(args)
        if len(tries) == 1:
            raise OSError("database is locked")
        return record_host(*args)
    monkeypatch.setattr(kanban_link, "record_host", flaky)
    delivery = f"far:host.unavailable:{int(START)}"
    far.down = True
    far.tick(3)
    assert _on_card(far.card)[0] == [] and not events.done(far.hub.store, delivery)

    far.tick(2)
    comments, _ = _on_card(far.card)
    assert len(comments) == 1 and comments[0].splitlines()[-1] == f"delivery {delivery}"
    assert len(far.chats()) == 1
    assert events.done(far.hub.store, delivery)
    assert [r["delivery"] for r in _receipts(far.hub, task_id, "notified")] == [delivery]


def _chat_fails_once(far: Far, monkeypatch) -> None:
    fail = far.tmp / "chat-fails-once"
    fail.touch()
    monkeypatch.setenv("FAKE_CHAT_FAIL_ONCE", str(fail))


def test_an_end_whose_chat_command_failed_goes_out_once_on_the_retry(far, monkeypatch):
    task_id = far.run(1, on_card=True)
    delivery = f"{task_id}:1:run.completed"
    _chat_fails_once(far, monkeypatch)
    far.hub.notify(task_id, "done", "1")
    assert far.chats() == [] and _on_card(far.card)[0] == [] and not events.done(far.hub.store, delivery)
    (failed,) = _receipts(far.hub, task_id, "notice_failed")
    assert failed["code"] == 1 and "the gateway is not running" in failed["stderr"]

    far.tick(2)
    (chat,) = far.chats()
    assert chat.startswith("run.completed (done) · ") and chat.splitlines()[-1] == f"delivery {delivery}"
    assert len(_on_card(far.card)[0]) == 1 and events.done(far.hub.store, delivery)
    assert [r["delivery"] for r in _receipts(far.hub, task_id, "notified")] == [delivery]


def test_an_outage_whose_chat_command_failed_goes_out_once_on_the_retry(far, monkeypatch):
    task_id = far.run(1, on_card=True)
    delivery = f"far:host.unavailable:{int(START)}"
    _chat_fails_once(far, monkeypatch)
    far.down = True
    far.tick(3)
    assert far.chats() == [] and not events.done(far.hub.store, delivery)
    assert len(_on_card(far.card)[0]) == 1  # the card took it: it is not told again

    far.tick(2)
    (chat,) = far.chats()
    assert chat.startswith("host.unavailable · ") and chat.splitlines()[-1] == f"delivery {delivery}"
    assert len(_on_card(far.card)[0]) == 1 and events.done(far.hub.store, delivery)
    assert [r["delivery"] for r in _receipts(far.hub, task_id, "notified")] == [delivery]


def test_a_late_retry_of_an_earlier_runs_end_leaves_the_run_going_now(far):
    task_id = far.run(1)
    unsaved = far.hub.store.load(task_id)
    far.hub.notify(task_id, "done", "1")
    far.hub.store.save({**unsaved, "runs": 2})  # the job's save of that end is lost; a follow-up started run 2
    far.remote[task_id] = {"state": "running", "run": 2, "alive": True}

    far.hub.notify(task_id, "done", "1")  # run 1's report, retried
    far.tick()

    job = far.hub.store.load(task_id)
    assert (job["state"], job["runs"]) == ("running", 2)
    assert [entry["delivery"] for entry in job["notified"]] == [f"{task_id}:1:run.completed"]
    assert len(far.chats()) == 1


def test_a_node_whose_runs_ended_after_an_outage_is_told_of_its_next_outage(far):
    first, second = far.run(1), far.run(2)
    far.down = True
    far.tick(3)
    far.hub.notify(first, "done", "1")  # the runs end; their device's reports reach the hub
    far.hub.notify(second, "done", "1")
    far.tick()

    third = far.run(3)
    far.tick(2)
    assert len(far.chats()) == 3
    far.tick()

    outage, _, _, again = far.chats()
    assert again.startswith("host.unavailable · ") and f"- {third} · " in again and first not in again
    assert again.splitlines()[-1] == f"delivery far:host.unavailable:{int(START + 4 * TICK_S)}"
    assert outage.splitlines()[-1] == f"delivery far:host.unavailable:{int(START)}"
