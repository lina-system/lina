"""``GET /api/lina/work/runs/{id}/record`` (lina#201): one worker run's record, round by round, over a scratch hub
(job files, receipts, fetched results, the runner's live view). Every response is validated against
``docs/api/board-client.yaml``; each test pins one of the issue's round rules."""

from __future__ import annotations

import datetime
import json
import os

import pytest
from fastapi.testclient import TestClient

from tests.hermes_cli._work_env import NOW, WorkEnv
from tests.hermes_cli.test_lina_board_api import DEVICES, Fakes, _valid

pytestmark = pytest.mark.platforms("posix")

S1, E1, S2, E2 = NOW - 3000, NOW - 2400, NOW - 1800, NOW - 1200


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.config(extra=DEVICES)
    return env


@pytest.fixture
def fakes(work, monkeypatch):
    return Fakes(work, monkeypatch)


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _receipt(fakes: Fakes, tid: str, kind: str, ts: float, **data) -> None:
    with (fakes.hub / "receipts.jsonl").open("a", encoding="utf-8") as fh:
        fh.write(json.dumps({"at": "", "ts": ts, "task": tid, "kind": kind, **data}) + "\n")


def _kept(fakes: Fakes, tid: str, name: str, text: str, at: float) -> None:
    """A fetched result file as the hub keeps it, with the time the run wrote it."""
    path = fakes.hub / "out" / "alpha" / tid / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    os.utime(path, (at, at))


def _record(client, tid: str) -> dict:
    response = client.get(f"/api/lina/work/runs/{tid}/record")
    assert response.status_code == 200, response.text
    return _valid("RunRecord", response.json())


def _two_rounds(fakes: Fakes, tid: str, *, first_end: bool = True, state: str = "done") -> None:
    fakes.job(tid, state=state, runs=2)
    _receipt(fakes, tid, "submitted", S1 - 60)
    _receipt(fakes, tid, "run_started", S1, run=1)
    if first_end:
        _receipt(fakes, tid, "exec_end", E1, state="done", run="1")
    _receipt(fakes, tid, "follow_up", S2, run=2, input_id=f"{tid}.2")


def test_rounds_come_newest_first_each_with_its_own_start_end_and_outcome(fakes, client):
    _two_rounds(fakes, "OT-ALP0001")
    _receipt(fakes, "OT-ALP0001", "exec_end", E2, state="done", run="2")
    _receipt(fakes, "OT-ALP0001", "verdict", E2 + 60, verdict="accepted", note="")
    _kept(fakes, "OT-ALP0001", "report.md", "# report\n\n- 두 기기를 비웠다\n", E2 - 30)
    _kept(fakes, "OT-ALP0001", "final.md", "기기를 비우고 마쳤다.\n", E2 - 10)

    rounds = _record(client, "OT-ALP0001")["rounds"]

    assert [(r["run"], r["startedAt"], r["endedAt"], r["endKnown"]) for r in rounds] == [
        (2, S2, E2, True), (1, S1, E1, True)]
    assert [(r["result"], r["verdict"], r["stopped"]) for r in rounds] == [
        ("두 기기를 비웠다", "accepted", False), (None, None, False)]
    assert rounds[0]["rows"] == [{"at": E2 - 10, "kind": "said", "text": "기기를 비우고 마쳤다.", "count": 1}]


def test_a_round_s_end_comes_only_from_its_own_end_receipt(fakes, client):
    _two_rounds(fakes, "OT-ALP0002", first_end=False, state="failed")
    _receipt(fakes, "OT-ALP0002", "exec_end", E2, state="failed", run="2")

    first, second = _record(client, "OT-ALP0002")["rounds"][::-1]

    assert (first["endedAt"], first["endKnown"], first["stopped"]) == (None, False, False)  # never the next start
    assert (second["endedAt"], second["endKnown"], second["stopped"]) == (E2, True, True)



def test_the_first_round_takes_the_job_s_start_when_its_start_receipt_is_gone_and_a_follow_up_is_kept(fakes, client):
    """Run 1's ``run_started`` fell out of the receipts but the job keeps ``started``; run 2's follow-up is kept. The
    first round is run 1 from the job's start, not a round with no start."""
    fakes.job("OT-ALP0009", state="done", runs=2, created=S1 - 60)  # the job's started = S1
    _receipt(fakes, "OT-ALP0009", "exec_end", E1, state="done", run="1")
    _receipt(fakes, "OT-ALP0009", "follow_up", S2, run=2, input_id="OT-ALP0009.2")
    _receipt(fakes, "OT-ALP0009", "exec_end", E2, state="done", run="2")

    rounds = _record(client, "OT-ALP0009")["rounds"]

    assert [(r["run"], r["startedAt"], r["endedAt"], r["startUnknown"]) for r in rounds] == [
        (2, S2, E2, False), (1, S1, E1, False)]

def test_tool_steps_fold_to_a_count_without_names_and_a_repeated_sentence_is_one_row(fakes, client):
    fakes.job("OT-ALP0003", state="running")
    _receipt(fakes, "OT-ALP0003", "run_started", S1, run=1)
    steps = [("said", "시험을 돌리는 중"), ("tool", None), ("tool", None), ("tool", None), ("said", "결과를 기다리는 중"),
             ("said", "결과를 기다리는 중"), ("said", "결과를 기다리는 중"), ("tool", None)]
    tail = [{"at": S1 + 10 * n, "kind": kind, **({"text": text} if text else {})} for n, (kind, text) in enumerate(steps)]
    fakes.live["OT-ALP0003"] = {"state": "running", "live": {"tools": ["bash", "read"], "last": "결과를 기다리는 중",
                                                             "tail": tail}}

    record = _record(client, "OT-ALP0003")

    assert [(row["kind"], row["text"], row["count"]) for row in record["rounds"][0]["rows"]] == [
        ("said", "시험을 돌리는 중", 1), ("tools", None, 3), ("said", "결과를 기다리는 중", 3), ("tools", None, 1)]
    assert record["rounds"][0]["rows"][1]["at"] == S1 + 10
    assert "bash" not in json.dumps(record) and "read" not in json.dumps(record)


def test_a_row_belongs_to_the_round_going_at_its_time_and_a_gap_comment_stands_on_its_own(fakes, client):
    _two_rounds(fakes, "OT-ALP0004", state="cancelled")
    gap = (E1 + S2) / 2
    _receipt(fakes, "OT-ALP0004", "verdict", gap, verdict="rejected", note="범위를 줄여서 다시")
    _receipt(fakes, "OT-ALP0004", "cancel_requested", S2 + 60, why="오늘은 그만")
    _receipt(fakes, "OT-ALP0004", "exec_end", S2 + 90, state="cancelled", run="2")
    _kept(fakes, "OT-ALP0004", "final.md", "사이에 남은 말\n", gap + 10)  # said after round 1 ended, before round 2

    record = _record(client, "OT-ALP0004")

    second, first = record["rounds"]
    assert second["rows"] == [{"at": S2 + 60, "kind": "comment", "text": "오늘은 그만", "count": 1}]
    assert first["rows"] == [] and first["verdict"] == "rejected"
    assert record["comments"] == [{"at": gap, "kind": "comment", "text": "범위를 줄여서 다시", "count": 1}]
    assert "사이에 남은 말" not in json.dumps(record)


def test_a_round_whose_start_fell_out_of_history_is_served_only_with_content(fakes, client):
    fakes.job("OT-ALP0005", state="done", runs=2, started=None)  # neither run 1's receipt nor the job keeps its start
    _receipt(fakes, "OT-ALP0005", "exec_end", E1, state="done", run="1")  # run 1's start is not on record
    _receipt(fakes, "OT-ALP0005", "follow_up", S2, run=2)
    _receipt(fakes, "OT-ALP0005", "exec_end", E2, state="done", run="2")
    fakes.job("OT-ALP0006", state="done", runs=2, started=None)  # no start on record anywhere
    _receipt(fakes, "OT-ALP0006", "award", S1, host="here")  # prepares a round, holds nothing of its own
    _receipt(fakes, "OT-ALP0006", "follow_up", S2, run=2)
    _receipt(fakes, "OT-ALP0006", "exec_end", E2, state="done", run="2")

    cut = _record(client, "OT-ALP0005")["rounds"]
    bare = _record(client, "OT-ALP0006")["rounds"]

    assert [(r["run"], r["startUnknown"], r["startedAt"], r["endedAt"]) for r in cut] == [
        (2, False, S2, E2), (None, True, None, E1)]
    assert [(r["run"], r["startUnknown"]) for r in bare] == [(2, False)]



def test_a_run_with_no_start_on_record_keeps_its_ended_round(fakes, client):
    """No start receipt and no ``started`` on the job, but run 1's end and its result are kept: the round is served
    with ``startUnknown``, its end and its result, not dropped."""
    fakes.job("OT-ALP0010", state="done", started=None)
    _receipt(fakes, "OT-ALP0010", "exec_end", E1, state="failed", run="1")
    _kept(fakes, "OT-ALP0010", "report.md", "# report\n\n- 기기 하나에 닿지 못했다\n", E1 - 30)

    rounds = _record(client, "OT-ALP0010")["rounds"]

    assert [(r["run"], r["startUnknown"], r["startedAt"], r["endedAt"], r["stopped"]) for r in rounds] == [
        (None, True, None, E1, True)]

def test_the_run_card_ends_where_its_record_s_newest_round_ends(fakes, client):
    _two_rounds(fakes, "OT-ALP0007")
    _receipt(fakes, "OT-ALP0007", "exec_end", E2, state="done", run="2")
    _receipt(fakes, "OT-ALP0007", "exec_end", E2 + 300, state="done", run="1", stale=True)  # run 1's late report

    card = next(c for c in _valid("BoardSnapshot", client.get("/api/lina/board").json())["cards"]
                if c["id"] == "OT-ALP0007")

    assert card["endedAt"] == _record(client, "OT-ALP0007")["rounds"][0]["endedAt"] == E2


def test_the_record_and_the_card_serve_the_run_s_model_and_settings_revision(fakes, client):
    """lina#257: the model a run started with (without its alias) and the settings revision it was submitted under;
    both null for a run from before, or on the runner's own default."""
    model = {"provider": "prov", "id": "m-open", "thinking": "high", "alias": "builder", "source": "fallback:off"}
    fakes.job("OT-ALP0011", state="done", model=model, settings={"rev": "r3", "runner_source": "default",
                                                                  "model_source": "fallback:off", "model_state": "off"})
    fakes.job("OT-ALP0012", state="done")
    cards = {c["id"]: c for c in _valid("BoardSnapshot", client.get("/api/lina/board").json())["cards"]}

    served = {"provider": "prov", "id": "m-open", "thinking": "high", "source": "fallback:off"}
    for tid, want in (("OT-ALP0011", (served, "r3")), ("OT-ALP0012", (None, None))):
        found = _record(client, tid)
        assert (found["model"], found["settingsRevision"]) == want
        assert (cards[tid]["model"], cards[tid]["settingsRevision"]) == want


def test_the_record_names_the_card_that_started_the_run_by_its_work_card_id(fakes, client):
    """A run handed out from a card names that card as ``/work/cards`` serves it (``hermes:`` on a Hermes card); a run
    no card started names none."""
    fakes.job("OT-ALP0013", card={"task": "t_3cb98fe8", "board": "main", "kind": "hermes"})
    fakes.job("OT-ALP0014", card={"task": "t_own", "board": None})
    fakes.job("OT-ALP0015")

    assert [_record(client, tid)["cardId"] for tid in ("OT-ALP0013", "OT-ALP0014", "OT-ALP0015")] == [
        "hermes:t_3cb98fe8", "t_own", None]


def test_the_record_is_behind_the_auth_gate_and_answers_304_until_it_changes(work, fakes, client):
    from hermes_cli import web_server
    _two_rounds(fakes, "OT-ALP0008", first_end=True, state="running")
    path = "/api/lina/work/runs/OT-ALP0008/record"

    assert TestClient(web_server.app).get(path).status_code == 401
    first = client.get(path)
    assert first.status_code == 200
    _valid("RunRecord", first.json())
    work.clock[0] += 1  # a later snapshot time alone is no change
    assert client.get(path, headers={"If-None-Match": first.headers["etag"]}).status_code == 304
    _receipt(fakes, "OT-ALP0008", "exec_end", E2, state="done", run="2")
    changed = client.get(path, headers={"If-None-Match": first.headers["etag"]})
    assert changed.status_code == 200 and changed.headers["etag"] != first.headers["etag"]
    assert {client.get(f"/api/lina/work/runs/{tid}/record").status_code for tid in ("OT-NONE1", "x..y")} == {404}


def test_the_omo_live_view_tail_keeps_the_order_and_names_no_tool(work, tmp_path):
    from hermes_cli.worker.runners.base import Handle, Task
    from hermes_cli.worker.runners.omo import OmoRunner

    runner = OmoRunner("omo", {"bin": str(tmp_path / "omo"), "node": str(tmp_path / "node"),
                               "engine": str(tmp_path / "engine")}, root=work.root)
    entries = [{"timestamp": "2027-01-15T10:00:00+00:00", "message": {"role": "assistant", "content": [
                   {"type": "text", "text": "파일을 읽는 중"}, {"type": "toolCall", "name": "read"}]}},
               {"timestamp": "2027-01-15T10:00:05+00:00", "message": {"role": "toolResult", "content": [
                   {"type": "text", "text": "/secret/path contents"}]}},
               {"timestamp": "2027-01-15T10:00:09+00:00", "message": {"role": "assistant", "content": [
                   {"type": "toolCall", "name": "bash"}]}}]
    answers = {"state": {"action": "state", "state": {"lifecycle": {"state": "running", "seq": 2, "waitingOn": {}},
                                                      "isStreaming": True, "pendingMessageCount": 0,
                                                      "pendingQuestions": []}},
               "read": {"action": "read", "entries": entries}}
    runner.session = lambda args, timeout: answers[args[0]]

    view = runner.live_view(Handle(Task("OT-ALP0009", tmp_path / "task"), 1, {"ref": "ses-1"}))

    base = datetime.datetime(2027, 1, 15, 10, tzinfo=datetime.UTC).timestamp()
    assert view["tail"] == [{"at": base, "kind": "said", "text": "파일을 읽는 중"}, {"at": base, "kind": "tool"},
                            {"at": base + 9, "kind": "tool"}]
