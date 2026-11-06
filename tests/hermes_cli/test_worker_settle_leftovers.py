"""Regression cases from completed turns held open by engine receipts (#228)."""

import json
import os
from datetime import UTC, datetime

import pytest

from hermes_cli.worker.runners.base import Task
from hermes_cli.worker.runners.omo_settle import Settle


@pytest.fixture(autouse=True)
def isolated_process_table(monkeypatch):
    monkeypatch.setattr("hermes_cli.worker.runners.omo_processes._process_table", dict)


def session_case(tmp_path, monkeypatch, *, source="terminal-monitors", human=None, dialogs=None,
                 report_time=950, role="assistant", stop="stop"):
    monkeypatch.setattr("hermes_cli.worker.runners.omo_settle.time.time", lambda: 1000)
    task = Task("OT-LEFTOVERS", tmp_path)
    task.work.mkdir()
    task.path("started.1").write_text("900")
    report = task.work / "report.md"
    report.write_text("completed")
    os.utime(report, (report_time, report_time))
    task.path("settle.1.json").write_text(json.dumps({
        "start": 900, "quiet_since": 900, "last_key": None, "errs": 0, "wakes": 0,
    }))
    state = {"lifecycle": {"state": "waiting_background", "waitingOn": {
        "background": [{"source": source, "activeCount": 1,
                        "items": [{"id": "leftover", "startedAt": "2026-01-01T00:00:00Z"}]}],
        "human": human or [], "children": 0}}, "isStreaming": False,
        "pendingDialogs": dialogs or [], "pendingQuestions": []}
    calls = []

    def call(argv, timeout):
        calls.append(argv[0])
        if argv[0] == "state":
            return {"action": "state", "state": state}
        if argv[0] == "read":
            return {"action": "read", "entries": [{
                "timestamp": datetime.fromtimestamp(900, UTC).isoformat(),
                "message": {"role": role, "stopReason": stop,
                            "content": [{"type": "text", "text": "Finished"}]}}]}
        return {"action": argv[0]}

    return Settle(task, 1, "session-test", call,
                  {"answered_grace_s": 10, "stall_grace_s": 600, "human_max_s": 120}), state, calls


@pytest.mark.parametrize("source", ["terminal-monitors", "delivery:notification"])
def test_answer_report_and_only_receipts_finish_after_grace(tmp_path, monkeypatch, source):
    settle, _, _ = session_case(tmp_path, monkeypatch, source=source)
    assert settle.step() == "done"


def test_pending_dialog_is_a_real_question_even_without_human_receipt(tmp_path, monkeypatch):
    settle, _, _ = session_case(tmp_path, monkeypatch, dialogs=[{"id": "dialog"}])
    assert settle.step() == "waiting_human"


def test_synthetic_question_is_not_an_open_question(tmp_path, monkeypatch):
    settle, state, _ = session_case(tmp_path, monkeypatch,
                                  human=[{"kind": "question", "requestId": "question:1"}])
    state["lifecycle"]["state"] = "waiting_human"
    settle.task.work.joinpath("report.md").unlink()
    assert settle.step() == "running"


def test_real_question_uses_bounded_clock_despite_new_notices(tmp_path, monkeypatch):
    settle, _, calls = session_case(tmp_path, monkeypatch,
                                   human=[{"kind": "question", "requestId": "dialog-1"}])
    settle.mem["human_since"] = 800
    settle.mem["last_entry_at"] = 999
    assert settle.step() == "stalled"
    assert "abort" in calls


@pytest.mark.parametrize("changes", [{"report_time": 800}, {"role": "toolResult"}, {"stop": "error"},
                                      {"source": "senpi-codemode"}])
def test_old_report_or_unfinished_work_cannot_close(tmp_path, monkeypatch, changes):
    settle, _, _ = session_case(tmp_path, monkeypatch, **changes)
    assert settle.step() == "running"


def test_live_work_prevents_sealed_engine_from_closing_run(tmp_path, monkeypatch):
    settle, state, _ = session_case(tmp_path, monkeypatch)
    state["lifecycle"]["state"] = "done"
    monkeypatch.setattr("hermes_cli.worker.runners.omo_settle.live_work_processes",
                        lambda task: [{"pid": 123, "cmd": "build"}], raising=False)
    assert settle.step() == "running"


def test_new_question_after_answered_live_work_gets_its_own_clock(tmp_path, monkeypatch):
    settle, state, calls = session_case(tmp_path, monkeypatch, dialogs=[{"id": "first"}])
    now, work = [1000], []
    monkeypatch.setattr("hermes_cli.worker.runners.omo_settle.time.time", lambda: now[0])
    monkeypatch.setattr("hermes_cli.worker.runners.omo_settle.live_work_processes", lambda task: work)
    assert settle.step() == "waiting_human"
    now[0] = 1050
    state["pendingDialogs"] = []
    work.append({"pid": 123, "cmd": "build"})
    assert settle.step() == "running"

    # A fresh poll object reads the clock persisted by the live-work path.
    now[0] = 1200
    state["pendingDialogs"] = [{"id": "second"}]
    work.clear()
    resumed = Settle(settle.task, 1, settle.ref, settle.call, settle.limits)
    assert resumed.step() == "waiting_human"
    assert "abort" not in calls
    assert json.loads(resumed.file.read_text())["human_since"] == 1200
