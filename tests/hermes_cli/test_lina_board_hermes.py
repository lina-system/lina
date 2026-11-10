"""The read-only ``hermes`` board source (lina#481) over a scratch LINA root and a fake Hermes CLI.

The fake CLI is a script outside the LINA tree that logs its argv and answers from ``canned.json`` under the configured
Hermes home, next to a ``kanban.db`` LINA must never open. Replies are checked against the published schemas
(``docs/api/work-client.yaml``, ``docs/api/board-client.yaml``).
"""

from __future__ import annotations

import datetime
import importlib.util
import json
import os
import sys
from pathlib import Path

import jsonschema
import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

import hermes_yaml
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli import lina_board, lina_board_hermes, lina_ocx
from tests.hermes_cli._work_env import NOW, WorkEnv

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]
WORK = "/api/plugins/kanban/work"
BOARD = "agents"
FAKE = """#!{python}
import json, os, sys, time
here = os.path.dirname(os.path.abspath(__file__))
with open(os.path.join(here, "calls.log"), "a", encoding="utf-8") as log:
    log.write(json.dumps(sys.argv[1:]) + "\\n")
with open(os.path.join(os.environ["HERMES_HOME"], "canned.json"), encoding="utf-8") as f:
    canned = json.load(f)
if canned.get("mode") == "fail":
    sys.stderr.write("kanban: database is locked\\n")
    sys.exit(3)
if canned.get("mode") == "hang":
    time.sleep(30)
args = sys.argv[1:]
if args == ["json"]:
    print(json.dumps(canned["work"]))
elif args == ["kanban", "--board", "{board}", "list", "--json"]:
    print(json.dumps(canned["list"]))
elif args[:4] == ["kanban", "--board", "{board}", "show"] and args[5:] == ["--json"]:
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
        (self.home / "canned.json").write_text(json.dumps(dict(data, mode=mode)), encoding="utf-8")

    def on(self, **extra) -> None:
        settings = {"command": [str(self.cli)], "board": BOARD, "work": [str(self.cli)], "home": str(self.home),
                    "cache_seconds": 0, **extra}
        self.work.config(extra="board:\n  hermes: " + json.dumps(settings) + "\n")

    def off(self) -> None:
        self.work.config()

    def calls(self) -> list[list[str]]:
        log = self.bin / "calls.log"
        return [json.loads(line) for line in log.read_text(encoding="utf-8").splitlines()] if log.exists() else []


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: env.root)
    monkeypatch.setattr(lina_board, "devices_read", lambda root, now, errors: [])
    monkeypatch.setattr(lina_board, "quota_read", lambda root, now: [])
    (env.root / "worker" / "hub" / "jobs").mkdir(parents=True)
    env.goal_and_todos(todos=1)
    kb.init_db()
    with kbc.connect_closing() as conn:
        kb.create_task(conn, title="Own card", goal_mode=True, assignee="alpha")
    lina_board_hermes._CACHE.clear()
    return env


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


def test_a_reply_is_kept_for_cache_seconds(hermes, client):
    hermes.on(cache_seconds=60)
    for _ in range(3):
        _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")
        _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
    assert sorted(map(tuple, hermes.calls())) == sorted([
        ("json",), ("kanban", "--board", BOARD, "list", "--json"),
        *[("kanban", "--board", BOARD, "show", i, "--json") for i in ("t_a1", "t_g1", "t_b1")]])


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
    assert {tuple(call[:4]) if call[0] == "kanban" else tuple(call) for call in hermes.calls()} == {
        ("json",), ("kanban", "--board", BOARD, "list"), ("kanban", "--board", BOARD, "show")}


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
    assert [c for c in hermes.calls()[len(calls):] if c not in (["json"],)] == []


@pytest.mark.parametrize(("mode", "message"), [("fail", "exited 3: kanban: database is locked"),
                                               ("hang", "timed out after 0.5s")])
def test_a_failing_command_is_one_error_and_the_other_sources_are_served(hermes, client, mode, message):
    hermes.on(timeout_seconds=0.5)
    hermes.canned(_canned(), mode=mode)
    board = _get(client, "/api/lina/board", "board-client.yaml", "BoardSnapshot")
    assert [(e["source"], message in e["message"]) for e in board["errors"]] == [("hermes", True)]
    assert board["inbox"]["mine"] == [] and board["counts"]["inbox"] == 0
    assert [row["id"] for row in board["due"]["today"] + board["due"]["overdue"]] == []
    cards = _get(client, f"{WORK}/cards", "work-client.yaml", "WorkCardList")
    assert [c["title"] for c in cards["cards"]] == ["Own card"]
    assert [(e["source"], message in e["message"]) for e in cards["errors"]] == [("hermes", True)]
    assert client.get(f"{WORK}/cards/hermes:t_a1").status_code == 503


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
    assert hermes.calls() == [] and "errors" not in before["cards"]

    hermes.on()
    on = replies()
    assert "hermes:t_a1" in [c["id"] for c in on["cards"]["cards"]]
    assert "hermes:T1" in [m["id"] for m in on["board"]["inbox"]["mine"]]
    ran = len(hermes.calls())

    hermes.off()
    assert replies() == before
    assert len(hermes.calls()) == ran
    assert _files(work.root, hermes.home) == files
