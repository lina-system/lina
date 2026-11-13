"""The read-only ``hermes`` board source (lina#481) over a scratch LINA root and a fake Hermes CLI.

The fake CLI is a script outside the LINA tree that logs its argv and answers from ``canned.json`` under the configured
Hermes home, next to a ``kanban.db`` LINA must never open. Its ``kanban watch`` prints one event each time a ``poke``
file appears there, for the card the file names (``t_a1`` when it is empty). The board is read in the background (lina#533): ``Hermes.on`` waits for the first pass. Replies are checked against the published schemas
(``docs/api/work-client.yaml``, ``docs/api/board-client.yaml``).
"""

from __future__ import annotations

import datetime
import importlib.util
import json
import os
import sys
import threading
import time
from pathlib import Path

import jsonschema
import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

import hermes_yaml
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli import lina_board, lina_board_hermes, lina_deps
from tests.hermes_cli._work_env import NOW, WorkEnv
from tests.waiting import wait_until

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]
WORK = "/api/plugins/kanban/work"
BOARD = "agents"
FAKE = """#!{python}
import json, os, sys, time
here = os.path.dirname(os.path.abspath(__file__))
with open(os.path.join(here, "calls.log"), "a", encoding="utf-8") as log:
    log.write(json.dumps(sys.argv[1:]) + "\\n")
args = sys.argv[1:]
if args == ["kanban", "--board", "{board}", "watch", "--interval", "2"]:
    print("Watching kanban events (initial board '{board}'). Ctrl-C to stop.", flush=True)
    poke = os.path.join(os.environ["HERMES_HOME"], "poke")
    while True:
        if os.path.exists(poke):
            with open(poke, encoding="utf-8") as f:
                card = f.read().strip() or "t_a1"
            os.remove(poke)
            print("[2026-11-11 09:30] " + card.ljust(10) + " commented          (@alpha)", flush=True)
        time.sleep(0.05)
with open(os.path.join(os.environ["HERMES_HOME"], "canned.json"), encoding="utf-8") as f:
    canned = json.load(f)
if canned.get("mode") == "fail":
    sys.stderr.write("kanban: database is locked\\n")
    sys.exit(3)
if canned.get("mode") == "hang":
    time.sleep(30)
if args == ["json"]:
    print(json.dumps(canned["work"]))
elif args == ["kanban", "--board", "{board}", "list", "--json"]:
    print(json.dumps(canned["list"]))
elif args[:4] == ["kanban", "--board", "{board}", "show"] and args[5:] == ["--json"]:
    time.sleep(canned.get("delay", 0))
    print(json.dumps(canned["show"][args[4]]))
else:
    sys.exit(2)
"""


def _schema(node):
    if isinstance(node, list):
        return [_schema(n) for n in node]
    if not isinstance(node, dict):
        return node
    out = {k: _schema(v) for k, v in node.items() if k != "nullable"}
    if node.get("nullable") and "type" in out:
        out["type"] = [out["type"], "null"]
    elif node.get("nullable"):
        out = {"anyOf": [out, {"type": "null"}]}
    return out


_COMPONENTS = {name: _schema(hermes_yaml.safe_load((REPO / "docs/api" / name).read_text(encoding="utf-8"))["components"])
               for name in ("work-client.yaml", "board-client.yaml")}


def _valid(spec: str, schema_name: str, data):
    jsonschema.Draft7Validator({"$ref": f"#/components/schemas/{schema_name}",
                                "components": _COMPONENTS[spec]}).validate(data)
    return data


def _task(task_id: str, title: str, status: str, assignee: str, body: str = "") -> dict:
    return {"id": task_id, "title": title, "body": body, "assignee": assignee, "status": status, "priority": 0,
            "tenant": None, "workspace_kind": "scratch", "workspace_path": None, "branch_name": None,
            "project_id": None, "created_by": "owner", "created_at": NOW - 9000, "started_at": NOW - 7200,
            "completed_at": None, "result": None, "skills": [], "goal_gates": []}


def _canned() -> dict:
    work = _task("t_a1", "Ship the importer", "running", "alpha", "## To-dos\n- [x] parse\n- [ ] load\n")
    goal = _task("t_g1", "Synthetic goal", "todo", "alpha")
    asks = _task("t_b1", "Pick a colour", "blocked", "beta", "## To-dos\n- [ ] choose\n")
    today = datetime.datetime.fromtimestamp(NOW, tz=datetime.UTC).astimezone().date().isoformat()
    return {
        "list": [work, goal, asks],
        "show": {
            "t_a1": {"task": work, "latest_summary": None, "parents": [], "children": ["t_g1"],
                     "comments": [{"author": "alpha", "body": "loading the second batch", "created_at": NOW - 60}],
                     "events": [], "runs": [{"id": 7, "profile": "alpha", "step_key": None, "status": "running",
                                             "outcome": None, "summary": None, "error": None, "metadata": None,
                                             "worker_pid": 4242, "started_at": NOW - 7200, "ended_at": None}]},
            "t_g1": {"task": goal, "latest_summary": None, "parents": ["t_a1"], "children": [], "comments": [],
                     "events": [], "runs": []},
            "t_b1": {"task": asks, "latest_summary": None, "parents": [], "children": [],
                     "comments": [{"author": "beta", "body": "질문: red or blue?", "created_at": NOW - 30}],
                     "events": [{"kind": "blocked", "payload": {"reason": "which colour?"}, "created_at": NOW - 30,
                                 "run_id": None}], "runs": []},
        },
        "work": {"version": 3, "bots": {"gamma": "감마"}, "items": [
            {"id": "G1", "kind": "목표", "title": "Old record goal", "by": "gamma", "state": "open", "due": today,
             "created": NOW - 100, "decide": "go or wait?", "comments": []},
            {"id": "T1", "kind": "할 일", "title": "Sign the form", "by": "owner", "from": "gamma", "state": "open",
             "owner": "owner", "created": NOW - 50, "comments": []},
        ]},
    }


class Hermes:
    """The fake Hermes CLI, its home with a database file nothing may open, and the LINA root's config for it."""

    def __init__(self, tmp_path: Path, work: WorkEnv):
        self.work = work
        self.home = tmp_path / "hermes-home"
        self.home.mkdir()
        (self.home / "kanban.db").write_bytes(b"not for LINA")
        self.bin = tmp_path / "fake-bin"
        self.bin.mkdir()
        self.cli = self.bin / "hermes"
        self.cli.write_text(FAKE.replace("{python}", sys.executable).replace("{board}", BOARD), encoding="utf-8")
        self.cli.chmod(0o755)
        self.canned(_canned())

    def canned(self, data: dict, mode: str | None = None) -> None:
        """The fake's answers, replaced whole so a running pass never reads half a file."""
        part = self.home / "canned.json.part"
        part.write_text(json.dumps(dict(data, mode=mode)), encoding="utf-8")
        os.replace(part, self.home / "canned.json")

    def on(self, wait: bool = True, **extra) -> None:
        """Set the source; with *wait*, start its reader and wait for the first pass to end."""
        settings = {"command": [str(self.cli)], "board": BOARD, "work": [str(self.cli)], "home": str(self.home),
                    **extra}
        self.work.config(extra="board:\n  hermes: " + json.dumps(settings) + "\n")
        if wait:
            assert lina_board_hermes.reader().wait(1, timeout=30)

    def poke(self, card: str = "") -> None:
        """What a change on the board makes ``kanban watch`` print."""
        (self.home / "poke").write_text(card, encoding="utf-8")

    def off(self) -> None:
        self.work.config()

    def calls(self) -> list[list[str]]:
        log = self.bin / "calls.log"
        return [json.loads(line) for line in log.read_text(encoding="utf-8").splitlines()] if log.exists() else []

    def reads(self) -> list[list[str]]:
        """The calls of the passes, without the long-running ``kanban watch``."""
        return [c for c in self.calls() if c[3:4] != ["watch"]]

    def shows(self) -> list[str]:
        return [c[4] for c in self.calls() if c[3:4] == ["show"]]


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_deps, "lina_root", lambda: env.root)
    monkeypatch.setattr(lina_board, "devices_read", lambda root, now, errors: [])
    monkeypatch.setattr(lina_board, "quota_read", lambda root, now: [])
    (env.root / "worker" / "hub" / "jobs").mkdir(parents=True)
    env.goal_and_todos(todos=1)
    kb.init_db()
    with kbc.connect_closing() as conn:
        kb.create_task(conn, title="Own card", goal_mode=True, assignee="alpha")
    yield env
    lina_board_hermes.stop()


@pytest.fixture
def hermes(tmp_path, work):
    return Hermes(tmp_path, work)


@pytest.fixture
def client(monkeypatch):
    from hermes_cli.web_routers import lina_board as board_routes
    spec = importlib.util.spec_from_file_location(
        "hermes_dashboard_plugin_kanban_hermes_test", REPO / "plugins/kanban/dashboard/plugin_api.py")
    mod = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, spec.name, mod)
    spec.loader.exec_module(mod)
    app = FastAPI()
    app.include_router(mod.router, prefix="/api/plugins/kanban")
    app.include_router(board_routes.router)
    return TestClient(app)


def _get(client, path: str, spec: str, schema: str, **params) -> dict:
    response = client.get(path, params=params)
    assert response.status_code == 200, response.text
    return _valid(spec, schema, response.json())


def _lines(work: WorkEnv) -> int:
    from hermes_cli.work import ledger
    db = ledger.open_db()
    try:
        return db.execute("SELECT count(*) FROM event").fetchone()[0]
    finally:
        db.close()


def _own_comments() -> int:
    with kbc.connect_closing() as conn:
        return conn.execute("SELECT count(*) FROM task_comments").fetchone()[0]


def test_hermes_cards_and_the_old_record_are_served_in_the_existing_shapes(hermes, client):
    hermes.on()
    cards = _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
    assert [c["title"] for c in cards["cards"]] == ["Own card", "Ship the importer", "Pick a colour"]
    assert cards["errors"] == []
    importer = cards["cards"][1]
    assert importer["id"] == "hermes:t_a1" and importer["readOnly"] is True and "readOnly" not in cards["cards"][0]
    assert (importer["state"], importer["agent"], importer["todoProgress"]) == ("in_progress", "alpha",
                                                                              {"done": 1, "total": 2})
    assert importer["now"] == "loading the second batch"
    assert [c["id"] for c in _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList",
                                  state="stopped")["cards"]] == ["hermes:t_b1"]

    goals = _get(client, f"{WORK}/goals", "work-client.yaml", "GoalList")["goals"]
    assert [(g["id"], [c["id"] for c in g["workCards"]], g["readOnly"]) for g in goals] == [
        ("hermes:t_g1", ["hermes:t_a1"], True)]
    assert _get(client, f"{WORK}/goals/hermes:t_g1", "work-client.yaml", "WorkGoal")["title"] == "Synthetic goal"
    detail = _get(client, f"{WORK}/cards/hermes:t_a1", "work-client.yaml", "WorkCardDetail")
    assert detail["readOnly"] is True and [r["live"] for r in detail["runs"]] == [True]
    assert client.get(f"{WORK}/cards/hermes:t_gone").status_code == 404

    inbox = _get(client, f"{WORK}/inbox", "work-client.yaml", "Inbox")
    assert [(b["cardId"], b["blockReason"], b["readOnly"]) for b in inbox["blocked"]] == [
        ("hermes:t_b1", "which colour?", True)]
    assert [(q["cardId"], q["body"]) for q in inbox["questions"]] == [("hermes:t_b1", "질문: red or blue?")]

    board = _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")
    assert board["errors"] == []
    assert [(d["target"], d["text"], d["agent"], d["readOnly"]) for d in board["inbox"]["decide"]] == [
        ("hermes:G1", "go or wait?", "gamma", True)]
    assert [(m["id"], m["agent"], m["readOnly"]) for m in board["inbox"]["mine"]] == [("hermes:T1", "gamma", True)]
    assert [row["id"] for row in board["due"]["today"]] == ["hermes:G1"]
    assert board["counts"]["agents"]["gamma"] == 0
    due = _get(client, "/api/lina/work/due", "board-client.yaml", "DueView")
    assert [row["id"] for row in due["due"]["today"]] == ["hermes:G1"]
    assert ["json"] in hermes.calls()
    assert ["kanban", "--board", BOARD, "list", "--json"] in hermes.calls()
    assert ["kanban", "--board", BOARD, "show", "t_a1", "--json"] in hermes.calls()


def test_a_request_runs_no_command_and_one_pass_reads_each_card_once(hermes, client):
    hermes.on()
    ran = hermes.reads()
    assert sorted(map(tuple, ran)) == sorted([
        ("json",), ("kanban", "--board", BOARD, "list", "--json"),
        *[("kanban", "--board", BOARD, "show", i, "--json") for i in ("t_a1", "t_g1", "t_b1")]])
    for _ in range(3):
        _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")
        _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
        _get(client, f"{WORK}/cards/hermes:t_a1", "work-client.yaml", "WorkCardDetail")
    assert hermes.reads() == ran


_PATH_EVENTS = {"open", "sqlite3.connect", "sqlite3.connect/handle", "os.listdir", "os.scandir", "glob.glob",
                "os.chdir", "shutil.copyfile", "os.rename", "os.remove"}
_WATCH: list | None = None


def _audit(event: str, args: tuple) -> None:
    if _WATCH is not None and event in _PATH_EVENTS and args and isinstance(args[0], (str, bytes, os.PathLike)):
        _WATCH.append((event, os.fsdecode(args[0])))


sys.addaudithook(_audit)


def test_every_read_goes_through_the_command_and_no_hermes_file_is_opened(hermes, client):
    global _WATCH
    hermes.on()
    _WATCH = []
    try:
        listed = _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
        _get(client, f"{WORK}/goals", "work-client.yaml", "GoalList")
        _get(client, f"{WORK}/cards/hermes:t_b1", "work-client.yaml", "WorkCardDetail")
        _get(client, f"{WORK}/inbox", "work-client.yaml", "Inbox")
        board = _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")
        opened = list(_WATCH)
    finally:
        _WATCH = None
    assert "hermes:t_a1" in [c["id"] for c in listed["cards"]]
    assert "hermes:T1" in [m["id"] for m in board["inbox"]["mine"]]
    home = str(hermes.home.resolve())
    assert opened and [p for _, p in opened if os.path.abspath(p).startswith(home)] == []
    assert {tuple(call[:4]) if call[0] == "kanban" else tuple(call) for call in hermes.calls()} <= {
        ("json",), ("kanban", "--board", BOARD, "list"), ("kanban", "--board", BOARD, "show"),
        ("kanban", "--board", BOARD, "watch")}


@pytest.mark.parametrize(("method", "path", "body"), [
    ("answer", f"{WORK}/inbox/hermes:t_b1/answer", {"text": "blue"}),
    ("done", "/api/lina/work/items/hermes:T1/done", {}),
    ("comment", "/api/lina/work/items/hermes:G1/comments", {"text": "please go"}),
    ("quick add", "/api/lina/work/add", {"agent": "gamma", "text": "Call the bank"}),
], ids=lambda value: value if isinstance(value, str) and "/" not in value else None)
def test_write_routes_refuse_hermes_items_with_409_and_change_nothing(hermes, client, work, method, path, body):
    hermes.on()
    board = _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")
    assert board["inbox"]["mine"][0]["readOnly"] is True
    lines, comments, calls = _lines(work), _own_comments(), hermes.calls()
    response = client.post(path, json=body)
    assert response.status_code == 409, (method, response.text)
    assert response.json()["detail"] == lina_board_hermes.READ_ONLY
    assert (_lines(work), _own_comments()) == (lines, comments)
    assert [c for c in hermes.calls()[len(calls):] if c[0] != "json" and c[3:4] not in (["list"], ["show"], ["watch"])] == []


@pytest.mark.parametrize(("mode", "message"), [("fail", "exited 3: kanban: database is locked"),
                                               ("hang", "timed out after 0.5s")])
def test_a_failing_command_is_one_error_and_the_other_sources_are_served(hermes, client, mode, message):
    hermes.canned(_canned(), mode=mode)
    hermes.on(timeout_seconds=0.5)
    board = _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")
    assert [(e["source"], message in e["message"]) for e in board["errors"]] == [("hermes", True)]
    assert board["inbox"]["mine"] == [] and board["counts"]["inbox"] == 0
    assert [row["id"] for row in board["due"]["today"] + board["due"]["overdue"]] == []
    cards = _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
    assert [c["title"] for c in cards["cards"]] == ["Own card"]
    assert [(e["source"], message in e["message"]) for e in cards["errors"]] == [("hermes", True)]
    assert client.get(f"{WORK}/cards/hermes:t_a1").status_code == 503


def test_the_old_record_is_served_when_lina_s_own_record_fails(hermes, client, monkeypatch):
    def broken(_root):
        raise RuntimeError("store unavailable")
    monkeypatch.setattr(lina_board, "work_read", broken)
    hermes.on()
    board = _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")
    assert [e["source"] for e in board["errors"]] == ["work"]
    assert [d["target"] for d in board["inbox"]["decide"]] == ["hermes:G1"]
    assert [m["id"] for m in board["inbox"]["mine"]] == ["hermes:T1"]
    assert [row["id"] for row in board["due"]["today"]] == ["hermes:G1"]
    assert (board["counts"]["inbox"], board["counts"]["due"], board["counts"]["unread"]) == (None, None, None)
    due = _get(client, "/api/lina/work/due", "board-client.yaml", "DueView")
    assert ([e["source"] for e in due["errors"]], due["count"]) == (["work"], None)
    assert [row["id"] for row in due["due"]["today"]] == ["hermes:G1"]


def test_with_only_the_old_record_set_no_card_read_runs(hermes, client):
    hermes.on(command=None)
    for path, schema in (("cards", "WorkCardList"), ("goals", "GoalList"), ("inbox", "Inbox")):
        reply = _get(client, f"{WORK}/{path}", "work-client.yaml", schema)
        assert reply["errors"] == [] and "hermesAt" not in reply, path
    assert hermes.calls() == [["json"]]
    board = _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")
    assert board["errors"] == [] and [m["id"] for m in board["inbox"]["mine"]] == ["hermes:T1"]


def _files(*roots: Path) -> set[Path]:
    return {p for root in roots for p in root.rglob("*") if p.is_file()}


def test_unset_nothing_runs_and_turning_it_off_leaves_no_file(hermes, client, work, tmp_path):
    def replies() -> dict:
        return {"cards": _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList"),
                "inbox": _get(client, f"{WORK}/inbox", "work-client.yaml", "Inbox"),
                "board": {k: v for k, v in _get(client, "/api/lina/board", "board-client.yaml",
                                                 "BoardSnapshot").items() if k != "at"}}
    before = replies()
    files = _files(work.root, hermes.home)
    assert hermes.calls() == [] and "errors" not in before["cards"] and "hermesAt" not in before["board"]

    hermes.on()
    on = replies()
    assert "hermes:t_a1" in [c["id"] for c in on["cards"]["cards"]]
    assert "hermes:T1" in [m["id"] for m in on["board"]["inbox"]["mine"]]
    ran = len(hermes.reads())

    hermes.off()
    assert replies() == before
    assert len(hermes.reads()) == ran
    assert _files(work.root, hermes.home) == files


def _done(task_id: str, ended: float) -> dict:
    return dict(_task(task_id, f"Old card {task_id}", "done", "alpha"), completed_at=int(ended))


def test_before_the_first_pass_ends_a_route_answers_at_once_with_one_error(hermes, client, monkeypatch):
    entered, release = threading.Barrier(3), threading.Event()

    def held(_src, _budget, _memo=None):
        entered.wait(timeout=30)
        release.wait(timeout=30)
        raise lina_board_hermes.SourceError("released")
    monkeypatch.setattr(lina_board_hermes, "read_cards", held)
    monkeypatch.setattr(lina_board_hermes, "read_record", held)
    try:
        hermes.on(wait=False)
        lina_board_hermes.reader()
        entered.wait(timeout=30)
        for path, spec, schema in ((f"{WORK}/cards", "work-client.yaml", "WorkCardList"),
                                   ("/api/lina/board", "board-client.yaml", "BoardSnapshot")):
            reply = _get(client, path, spec, schema)
            assert not release.is_set() and lina_board_hermes.reader().passes == 0, path
            assert [(e["source"], e["message"]) for e in reply["errors"]] == [
                ("hermes", lina_board_hermes.FIRST_PASS)]
            assert reply["hermesAt"] is None
        cards = _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
        assert [c["title"] for c in cards["cards"]] == ["Own card"]
        assert client.get(f"{WORK}/cards/hermes:t_a1").status_code == 503
        assert lina_board_hermes.reader().passes == 0
    finally:
        release.set()


def test_a_pass_shows_only_open_cards_and_cards_done_within_the_window(hermes, client):
    now = time.time()
    data = _canned()
    old = [_done(f"t_old{n}", now - 3 * lina_board.DONE_WINDOW_S) for n in range(300)]
    recent = [_done(f"t_new{n}", now - 3600) for n in range(2)]
    data["list"] += old + recent
    data["show"].update({row["id"]: {"task": row, "parents": [], "children": [], "comments": [], "events": [],
                                     "runs": []} for row in old + recent})
    hermes.canned(data)
    hermes.on()
    assert sorted(hermes.shows()) == sorted(["t_a1", "t_g1", "t_b1", "t_new0", "t_new1"])
    cards = _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")["cards"]
    detail = {c["id"]: c.get("detail") for c in cards if c["id"].startswith("hermes:t_")}
    assert [i for i, d in detail.items() if d is False] == [f"hermes:{row['id']}" for row in old]
    assert (detail["hermes:t_new0"], detail["hermes:t_a1"]) == (None, None)
    one = _get(client, f"{WORK}/cards/hermes:t_old7", "work-client.yaml", "WorkCardDetail")
    assert (one["detail"], one["state"], one["comments"]) == (False, "done", [])
    assert len(hermes.shows()) == 5


def _next_pass(running) -> None:
    """Wait for a whole pass that started after now."""
    assert running.wait(running.passes + 2, timeout=30)


def test_a_failing_pass_keeps_the_last_good_one_served_and_the_next_pass_tries_again(hermes, client, monkeypatch):
    hermes.on()
    good = _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
    assert good["errors"] == [] and good["hermesAt"] is not None
    hermes.canned(_canned(), mode="fail")
    monkeypatch.setattr(lina_board_hermes, "PASS_EVERY_S", 0.05)  # the old record is read again from now on
    hermes.poke()
    _next_pass(lina_board_hermes.reader())
    stale = _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
    assert [c["title"] for c in stale["cards"]] == [c["title"] for c in good["cards"]]
    assert stale["hermesAt"] == good["hermesAt"]
    assert [(e["source"], "exited 3" in e["message"], "serving the read of" in e["message"])
            for e in stale["errors"]] == [("hermes", True, True)]
    board = _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")
    assert [e["source"] for e in board["errors"]] == ["hermes"]
    assert [m["id"] for m in board["inbox"]["mine"]] == ["hermes:T1"]
    assert _get(client, f"{WORK}/cards/hermes:t_a1", "work-client.yaml", "WorkCardDetail")["readOnly"] is True

    hermes.canned(_canned())
    _next_pass(lina_board_hermes.reader())
    again = _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
    assert again["errors"] == [] and again["hermesAt"] is not None
    assert [c["title"] for c in again["cards"]] == [c["title"] for c in good["cards"]]


def test_an_event_of_kanban_watch_starts_the_next_pass_before_the_timer(hermes):
    hermes.on()
    started = time.monotonic()
    hermes.poke()
    assert lina_board_hermes.reader().wait(2, timeout=lina_board_hermes.PASS_EVERY_S / 2)
    assert time.monotonic() - started < lina_board_hermes.PASS_EVERY_S
    assert ["kanban", "--board", BOARD, "watch", "--interval", "2"] in hermes.calls()


def test_a_card_read_from_its_list_row_keeps_its_links_to_shown_cards(hermes, client):
    """An open goal whose only work card ended outside the window: the work card's ``children`` come from the goal's
    ``parents``, so the goal stays a goal (not a work card) with its work card, and no ``show`` is added for it."""
    data = _canned()
    old = dict(_done("t_oldw", time.time() - 3 * lina_board.DONE_WINDOW_S),
               body="## To-dos\n- [x] all\n", title="Old work")
    goal = dict(_task("t_g2", "Open goal", "todo", "alpha"))
    data["list"] += [old, goal]
    data["show"]["t_oldw"] = {"task": old, "parents": [], "children": ["t_g2"], "comments": [], "events": [],
                              "runs": []}
    data["show"]["t_g2"] = {"task": goal, "parents": ["t_oldw"], "children": [], "comments": [], "events": [],
                            "runs": []}
    hermes.canned(data)
    hermes.on()
    assert sorted(hermes.shows()) == sorted(["t_a1", "t_g1", "t_b1", "t_g2"])
    goals = {g["id"]: g for g in _get(client, f"{WORK}/goals", "work-client.yaml", "GoalList")["goals"]}
    assert [(c["id"], c.get("detail")) for c in goals["hermes:t_g2"]["workCards"]] == [("hermes:t_oldw", False)]
    one = _get(client, f"{WORK}/goals/hermes:t_g2", "work-client.yaml", "WorkGoal")
    assert one["title"] == "Open goal" and one.get("detail") is None
    cards = [c["id"] for c in _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")["cards"]]
    assert "hermes:t_g2" not in cards and "hermes:t_oldw" in cards
    assert len(hermes.shows()) == 4


def _open_cards(data: dict, count: int) -> list[str]:
    """*count* more running cards on the canned board, each with its ``show``; their ids."""
    rows = [_task(f"t_c{n:03d}", f"Card {n}", "running", "alpha") for n in range(count)]
    data["list"] += rows
    data["show"].update({row["id"]: {"task": row, "parents": [], "children": [], "comments": [], "events": [],
                                     "runs": []} for row in rows})
    return [row["id"] for row in rows]


def test_a_later_pass_shows_again_only_the_cards_that_changed(hermes, client, monkeypatch):
    """lina#573: a card whose list row did not change and that ``kanban watch`` did not name is not shown again."""
    monkeypatch.setattr(lina_board_hermes, "PASS_EVERY_S", 0.05)
    data = _canned()
    ids = _open_cards(data, 30)
    hermes.canned(data)
    hermes.on()
    running = lina_board_hermes.reader()
    first = len(hermes.shows())
    assert first == 33

    _next_pass(running)
    assert hermes.shows()[first:] == []

    data["list"][10] = dict(data["list"][10], status="blocked")
    data["show"][ids[7]]["task"] = data["list"][10]
    hermes.canned(data)
    _next_pass(running)
    assert hermes.shows()[first:] == [ids[7]]
    card = {c["id"]: c for c in _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")["cards"]}
    assert card[f"hermes:{ids[7]}"]["state"] == "stopped"

    data["show"][ids[20]]["comments"] = [{"author": "alpha", "body": "halfway there", "created_at": NOW}]
    hermes.canned(data)
    hermes.poke(ids[20])
    wait_until(lambda: hermes.shows()[first + 1:] == [ids[20]], timeout=30, what="the named card shown again")
    _next_pass(running)
    assert hermes.shows()[first:] == [ids[7], ids[20]]
    card = {c["id"]: c for c in _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")["cards"]}
    assert card[f"hermes:{ids[20]}"]["now"] == "halfway there"


def test_a_pass_cut_by_its_limit_leaves_its_shows_to_the_next(hermes, client, monkeypatch):
    """lina#573: a board too big for one pass's limit is still read whole, over the passes that follow."""
    monkeypatch.setattr(lina_board_hermes, "PASS_EVERY_S", 0.05)
    monkeypatch.setattr(lina_board_hermes, "PASS_LIMIT_S", 3.0)
    data = dict(_canned(), delay=0.5)
    ids = _open_cards(data, 40)
    hermes.canned(data)
    hermes.on()
    running = lina_board_hermes.reader()
    assert running.part("cards").error is not None and running.part("cards").value is None
    wait_until(lambda: running.part("cards").error is None, timeout=90, what="a pass that ends")
    cards = _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
    assert cards["errors"] == []
    assert {f"hermes:{raw}" for raw in ids} <= {c["id"] for c in cards["cards"]}
    assert len(hermes.shows()) < 2 * 43


def _hub_run(work: WorkEnv, run_id: str, card: str, **extra) -> None:
    """A hub run handed out from a Hermes card (``job.card`` as ``kanban_link.card_for`` writes it)."""
    job = {"id": run_id, "bot": "alpha", "topic": f"topic-{run_id}", "mode": "ulw", "state": "failed",
           "created": NOW - 3600, "started": NOW - 3500, "host": None, "prompt": "ulw do it\n", "verdict": None,
           "card": {"task": card, "board": BOARD, "kind": "hermes"}, "todo": None, **extra}
    (work.root / "worker" / "hub" / "jobs" / f"{run_id}.json").write_text(json.dumps(job), encoding="utf-8")


def test_a_run_names_its_card_by_the_id_work_cards_serves(work, hermes, client):
    """lina#630: ``RunCard.card`` is the card's id in /work/cards, source prefix included; a run whose card is not
    in the list still carries it."""
    _hub_run(work, "run-1", "t_b1")
    _hub_run(work, "run-2", "card-gone")
    hermes.on()
    runs = {r["id"]: r for r in _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")["cards"]}
    listed = {c["id"]: c for c in _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")["cards"]}
    assert (runs["run-1"]["card"], runs["run-1"]["board"]) == ("hermes:t_b1", BOARD)
    assert listed[runs["run-1"]["card"]]["title"] == "Pick a colour"
    assert runs["run-2"]["card"] == "hermes:card-gone" and runs["run-2"]["card"] not in listed
    assert _get(client, f"{WORK}/cards/{runs['run-1']['card']}", "work-client.yaml", "WorkCardDetail")["id"] == \
        "hermes:t_b1"


def _stopped_card(data: dict, raw: str, *payloads: dict, status: str = "blocked") -> None:
    """A work card of the fake board whose block events carry *payloads*, oldest first."""
    task = _task(raw, f"Card {raw}", status, "alpha", "## To-dos\n- [ ] one\n")
    events = [{"kind": "blocked", "payload": p, "created_at": NOW - 100 + n, "run_id": None}
              for n, p in enumerate(payloads)]
    data["list"].append(task)
    data["show"][raw] = {"task": task, "latest_summary": None, "parents": [], "children": [], "comments": [],
                         "events": events, "runs": []}


def test_a_stopped_hermes_card_says_why_it_stopped_and_who_acts_from_its_latest_block_event(hermes, client):
    """lina#629: reason, waitingOn and retry of a stopped card come from its latest block event, in the shapes the
    Hermes board writes; a card with no block event serves null, not a guess."""
    data = _canned()
    shapes = {
        "card-1": [{"kind": "needs_input", "reason": "pick one", "recurrences": 1, "source_status": "running"}],
        "card-2": [{"kind": None, "reason": "agent-a · 검토 · the draft is ready", "recurrences": 1,
                    "source_status": "running"}],
        "card-3": [{"kind": None, "reason": "agent-a · 돈·바깥 · an invoice to pay", "recurrences": 2,
                    "source_status": "running"}],
        "card-4": [{"kind": None, "reason": "agent-a · 갈림길 · keep or drop the cache",
                    "source_status": "running"}],
        "card-5": [{"kind": None, "reason": "The access key ran out.", "source_status": "running"}],
        "card-6": [{"kind": None, "reason": "Which region should it run in?", "source_status": "ready"}],
        "card-7": [{"kind": "dependency", "reason": "waits for the import card", "source_status": "running"}],
        "card-8": [{"kind": "needs_input", "reason": "pick one", "recurrences": 1, "source_status": "running"},
                   {"kind": None, "reason": "The access key ran out.", "recurrences": 1, "source_status": "running"}],
        "card-9": [],
        "card-11": [{"kind": "needs_input", "reason": "waits for the import card", "source_status": "running",
                     "wait_for": {"kind": "card", "task_id": "card-1"}}],
    }
    for raw, payloads in shapes.items():
        _stopped_card(data, raw, *payloads)
    # The failure breaker blocked card-11 after its wait was met: nothing wakes it, a person has to act.
    data["show"]["card-11"]["events"].append({"kind": "gave_up", "payload": {"failures": 3}, "created_at": NOW - 50, "run_id": None})
    _stopped_card(data, "card-10", status="scheduled")
    hermes.canned(data)
    hermes.on()
    cards = {c["id"]: c for c in _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")["cards"]}
    served = {raw: (cards[f"hermes:{raw}"]["state"], cards[f"hermes:{raw}"]["reason"],
                    cards[f"hermes:{raw}"]["waitingOn"], cards[f"hermes:{raw}"]["retry"])
              for raw in [*shapes, "card-10"]}
    assert served == {
        "card-1": ("stopped", "decision", "owner", "none"),
        "card-2": ("stopped", "review", "owner", "none"),
        "card-3": ("stopped", "feedback", "owner", "none"),
        "card-4": ("stopped", "decision", "owner", "none"),
        "card-5": ("stopped", "feedback", "owner", "none"),
        "card-6": ("stopped", "decision", "owner", "none"),
        "card-7": ("stopped", "blocked_by_item", None, "scheduled"),
        "card-8": ("stopped", "feedback", "owner", "none"),
        "card-9": ("stopped", None, None, None),
        "card-11": ("stopped", "failed", "owner", "none"),
        "card-10": ("planned", "scheduled", None, "scheduled"),
    }
    detail = _get(client, f"{WORK}/cards/hermes:card-2", "work-client.yaml", "WorkCardDetail")
    assert (detail["reason"], detail["waitingOn"], detail["retry"]) == ("review", "owner", "none")
