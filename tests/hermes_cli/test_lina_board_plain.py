"""Checked model-written card lines (lina#151) over a scratch LINA root with a fake auxiliary client: what passes its
field check is stored and served as ``plain`` on run cards (``/api/lina/board``) and work cards
(``/api/plugins/kanban/work/cards/{id}``), what fails is recorded and stays null, an unchanged source is never sent
again, and the board's ``writer`` state follows the calls. Responses validate against both client schemas.

``hermes_cli.lina_board_plain`` is imported inside each test so each one fails on its own before the module exists.
"""

from __future__ import annotations

import sqlite3
import threading
from pathlib import Path
from types import SimpleNamespace

import jsonschema
import pytest
from fastapi.testclient import TestClient

import hermes_yaml
from tests.hermes_cli._work_env import NOW, WorkEnv
from tests.hermes_cli.test_lina_board_api import Fakes

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]
GOOD = {"title": "빌드 기기 디스크 교체", "now": "시험 묶음을 돌리는 중", "result": "빌드 기기 두 대의 디스크를 비웠다."}


def _jsonschema(node):
    if isinstance(node, list):
        return [_jsonschema(n) for n in node]
    if not isinstance(node, dict):
        return node
    out = {k: _jsonschema(v) for k, v in node.items() if k != "nullable"}
    if node.get("nullable") and "type" in out:
        out["type"] = [out["type"], "null"]
    elif node.get("nullable"):
        out = {"anyOf": [out, {"type": "null"}]}
    return out


def _valid(spec: str, schema_name: str, data):
    components = _jsonschema(hermes_yaml.safe_load((REPO / "docs/api" / spec).read_text(encoding="utf-8"))["components"])
    jsonschema.Draft7Validator({"$ref": f"#/components/schemas/{schema_name}", "components": components}).validate(data)
    return data


class FakeModel:
    """Stands in for ``agent.auxiliary_client.call_llm``: answers per field, counts the calls, or fails."""

    def __init__(self):
        self.answers = dict(GOOD)
        self.calls: list[tuple[str, str]] = []
        self.fail = False

    def __call__(self, task=None, *, messages, **_kwargs):
        from hermes_cli.lina_board_plain import _PROMPTS
        assert task == "board_lines"
        field = next(name for name, prompt in _PROMPTS.items() if prompt in messages[0]["content"])
        self.calls.append((field, messages[1]["content"]))
        if self.fail:
            raise ConnectionError("the model is not reachable")
        return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=self.answers[field]))])


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.config(extra="devices:\n  workers:\n    here: {host: local, cap: 1}\n")
    return env


@pytest.fixture
def fakes(work, monkeypatch):
    return Fakes(work, monkeypatch)


@pytest.fixture
def model(monkeypatch):
    from agent import auxiliary_client
    fake = FakeModel()
    monkeypatch.setattr(auxiliary_client, "call_llm", fake)
    return fake


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _runs(work: WorkEnv, fakes: Fakes) -> None:
    """OT-ALP0001 running (its live view names what it does), OT-ALP0002 ended with a report."""
    fakes.job("OT-ALP0001", heading="Rotate the build machine disks before they fill")
    fakes.live["OT-ALP0001"] = {"state": "running", "live": {"dash": "working", "last": "running the tests, 120 of 400"}}
    fakes.job("OT-ALP0002", state="done", ended=NOW - 600, heading="Clear old logs")
    out = fakes.hub / "out" / "alpha" / "OT-ALP0002"
    out.mkdir(parents=True)
    (out / "report.md").write_text("# report\n\nCleared the logs on both build machines.\n\n## 만든 것\n- x\n",
                                   encoding="utf-8")


def _rows(work: WorkEnv) -> dict[tuple[str, str, str], tuple[str | None, str | None]]:
    with sqlite3.connect(work.root / "board" / "plain.db") as db:
        return {(k, i, f): (text, error) for k, i, f, text, error in db.execute("SELECT kind, id, field, text, error FROM line")}


def _card(work: WorkEnv) -> str:
    from hermes_cli import kanban_db as kb
    from hermes_cli import kanban_db_connect as kbc
    kb.init_db()
    conn = kbc.connect()
    try:
        tid = kb.create_task(conn, title="Weekly photo backup check", body="Closes lina-system/lina-app#9", goal_mode=True,
                             assignee="alpha")
        kb.add_comment(conn, tid, "alpha", "checking last week's backup on the photo server")
    finally:
        conn.close()
    return tid


@pytest.mark.parametrize(("field", "text", "reason"), [
    ("title", "빌드 기기 디스크 교체", None),
    ("title", "사진 보관함 주간 백업 점검하기", None),
    ("title", "빌드 기기들의 오래된 디스크를 새것으로 바꾸는 교체", "longer than 25 characters"),
    ("title", "G12 디스크 교체", "contains an id"),
    ("title", "OT-ALP0001 교체", "contains an id"),
    ("title", "build 기기 정리", "contains the Latin word build"),
    ("title", "GitHub 알림 정리", None),
    ("title", "디스크를 교체했다", "a sentence, not a title"),
    ("title", "디스크 교체 상황", "does not end on an action noun or a ~기 form"),
    ("now", "시험 묶음을 돌리는 중", None),
    ("now", "사진 크기 일괄 변환 대기", None),
    ("now", "시험을 돌렸다. 이제 고친다", "more than one sentence"),
    ("now", "시험을 돌렸다", "not the present progressive or a noun phrase"),
    ("now", "report.md 를 쓰는 중", "contains a file name"),
    ("now", "`시험` 을 돌리는 중", "contains a backtick"),
    ("now", "로그를 | 로 거르는 중", "contains a command"),
    ("result", "빌드 기기 두 대의 디스크를 비웠다.", None),
    ("result", "https://example.com 에 올렸다.", "contains a URL"),
    ("result", "기록을 /var/log 에서 지웠다.", "contains a path"),
    ("result", "커밋 3f2a9c1e 로 고쳤다.", "contains a hash"),
    ("result", "#123 을 닫았다.", "contains an id"),
    ("result", "가" * 81, "longer than 80 characters"),
])
def test_each_field_check_says_why_it_refuses(field, text, reason):
    from hermes_cli.lina_board_plain import CHECKS
    assert CHECKS[field](text) == reason


def test_good_answers_are_stored_and_served_on_run_cards_and_work_cards(work, fakes, model, client):
    from hermes_cli.lina_board_plain import write_board
    _runs(work, fakes)
    tid = _card(work)

    # two runs (a running one: title and now; an ended one: title and result) and one work card (title and now)
    assert write_board(work.root, NOW) == {"called": 6, "failed": 0, "pending": 0}

    sent = dict((f"{field}:{source}", None) for field, source in model.calls)
    assert any(key.startswith("title:") and "Rotate the build machine disks" in key for key in sent)
    assert any(key.startswith("now:") and "running the tests" in key for key in sent)
    assert any(key.startswith("result:") and "Cleared the logs" in key for key in sent)
    assert any(key.startswith("title:") and "lina-system/lina-app#9" in key for key in sent)
    board = _valid("board-client.yaml", "BoardSnapshot", client.get("/api/lina/board").json())
    cards = {card["id"]: card["plain"] for card in board["cards"]}
    assert cards["OT-ALP0001"] == {"title": GOOD["title"], "now": GOOD["now"], "result": None}
    assert cards["OT-ALP0002"] == {"title": GOOD["title"], "now": None, "result": GOOD["result"]}
    assert board["writer"] == {"state": "ok", "lastSuccessAt": NOW, "stoppedSince": None}
    card = _valid("work-client.yaml", "WorkCardDetail", client.get(f"/api/plugins/kanban/work/cards/{tid}").json())
    assert card["plain"] == {"title": GOOD["title"], "now": GOOD["now"], "result": None}


def test_reads_only_look_the_cache_up_and_never_call_the_model(work, fakes, model, client):
    _runs(work, fakes)
    board = _valid("board-client.yaml", "BoardSnapshot", client.get("/api/lina/board").json())
    assert model.calls == []
    assert {card["id"]: card["plain"] for card in board["cards"]} == {
        "OT-ALP0001": {"title": None, "now": None, "result": None},
        "OT-ALP0002": {"title": None, "now": None, "result": None}}
    assert board["writer"] == {"state": "never", "lastSuccessAt": None, "stoppedSince": None}


@pytest.mark.parametrize(("answer", "reason"), [
    ("", "empty"),
    ("빌드 기기들의 오래된 디스크를 새것으로 바꾸는 교체", "longer than 25 characters"),
    ("OT-ALP0001 디스크 교체", "contains an id"),
    ("Rotate build disks", "contains the Latin word Rotate"),
])
def test_an_empty_or_malformed_answer_is_recorded_and_the_field_stays_null(work, fakes, model, client, answer, reason):
    from hermes_cli.lina_board_plain import write_board
    _runs(work, fakes)
    model.answers["title"] = answer

    write_board(work.root, NOW)

    assert _rows(work)[("run", "OT-ALP0001", "title")] == (None, reason)
    plain = {card["id"]: card["plain"] for card in client.get("/api/lina/board").json()["cards"]}
    assert plain["OT-ALP0001"]["title"] is None and plain["OT-ALP0001"]["now"] == GOOD["now"]


def test_an_unchanged_source_makes_no_new_call_and_a_changed_one_makes_one(work, fakes, model):
    from hermes_cli.lina_board_plain import write_board
    _runs(work, fakes)
    model.answers["title"] = "OT-1 refused"
    write_board(work.root, NOW)
    first = len(model.calls)

    assert write_board(work.root, NOW + 60)["called"] == 0  # a refused answer is not asked again either
    assert len(model.calls) == first

    fakes.live["OT-ALP0001"]["live"]["last"] = "fixing the failing disk check"
    assert write_board(work.root, NOW + 120)["called"] == 1
    assert model.calls[-1][0] == "now" and "fixing the failing disk check" in model.calls[-1][1]


def test_the_writer_goes_stopped_when_calls_fail_and_ok_after_a_success(work, fakes, model, monkeypatch):
    from hermes_cli import lina_board_plain as plain
    _runs(work, fakes)
    assert plain.writer_state(work.root, NOW)["state"] == "never"

    model.fail = True
    assert plain.write_board(work.root, NOW)["failed"] == 4
    assert plain.writer_state(work.root, NOW + 1) == {"state": "stopped", "lastSuccessAt": None, "stoppedSince": NOW}
    assert ("run", "OT-ALP0001", "title") not in _rows(work)  # a failed call stores nothing and is tried again

    model.fail = False
    assert plain.write_board(work.root, NOW + 300)["called"] == 4
    assert plain.writer_state(work.root, NOW + 301) == {"state": "ok", "lastSuccessAt": NOW + 300, "stoppedSince": None}

    monkeypatch.setattr(plain, "MAX_CALLS", 1)
    fakes.live["OT-ALP0001"]["live"]["last"] = "new words"
    fakes.job("OT-ALP0003", heading="Another run")
    plain.write_board(work.root, NOW + 600)
    assert plain.writer_state(work.root, NOW + 601)["state"] == "ok"
    assert plain.writer_state(work.root, NOW + 600 + plain.STOP_AFTER_S + 1) == {
        "state": "stopped", "lastSuccessAt": NOW + 600, "stoppedSince": NOW + 600 + plain.STOP_AFTER_S}


def test_a_hub_tick_returns_promptly_while_the_model_blocks(work, fakes, monkeypatch):
    from agent import auxiliary_client
    from hermes_cli.worker import settings
    from hermes_cli.worker.hub import Hub
    _runs(work, fakes)
    entered, release, ticked = threading.Event(), threading.Event(), threading.Event()

    def blocking(*_args, **_kwargs):
        entered.set()
        release.wait(30)
        raise ConnectionError("released")
    monkeypatch.setattr(auxiliary_client, "call_llm", blocking)
    hub = Hub(settings.root(work.root), out=lambda *_: None)
    thread = threading.Thread(target=lambda: (hub.tick(), ticked.set()), daemon=True)
    try:
        thread.start()
        assert ticked.wait(10), "the hub tick waited on the model"
        assert not entered.is_set()
    finally:
        release.set()
        thread.join(30)


def test_the_service_s_writer_thread_writes_the_lines_and_stops_with_the_service(work, fakes, model):
    from hermes_cli import lina_board_plain as plain
    from hermes_cli.worker import settings
    _runs(work, fakes)
    stop = threading.Event()
    stop.set()  # the service is already stopping: the thread makes its one pass and ends
    thread = plain.start_writer(settings.root(work.root), stop)
    thread.join(30)
    assert not thread.is_alive()
    assert _rows(work)[("run", "OT-ALP0001", "title")] == (GOOD["title"], None)
    assert plain.writer_state(work.root, NOW)["state"] == "ok"


def test_a_pass_stops_at_its_wall_time_bound_and_leaves_the_rest_pending(work, fakes, model, monkeypatch):
    from hermes_cli import lina_board_plain as plain
    _runs(work, fakes)
    clock = [0.0]
    monkeypatch.setattr(plain, "_clock", lambda: clock[0])
    answer = model.__call__

    def slow(*args, **kwargs):
        clock[0] += 40  # each call takes 40 s of the pass's 90
        return answer(*args, **kwargs)
    from agent import auxiliary_client
    monkeypatch.setattr(auxiliary_client, "call_llm", slow)

    assert plain.write_board(work.root, NOW) == {"called": 3, "failed": 0, "pending": 1}
    assert len(_rows(work)) == 3
    assert plain.writer_state(work.root, NOW + 1)["state"] == "ok"
    assert plain.write_board(work.root, NOW + 60) == {"called": 1, "failed": 0, "pending": 0}
    assert len(_rows(work)) == 4
