"""A session the engine keeps at ``running`` and streaming after its work is done (lina#404): its recorded activity,
not the lifecycle, decides; empty ``delivery:*`` receipts do not hold a finished run open."""

import json
import os
from datetime import UTC, datetime

import pytest

from hermes_cli.worker import notice
from hermes_cli.worker.runners.base import Task
from hermes_cli.worker.runners.omo_settle import Settle

DELIVERIES = [{"source": f"delivery:{name}:notification:1", "activeCount": 1, "items": []}
              for name in ("omo-kibitzer", "senpi-terminal", "senpi-monitor")]


@pytest.fixture(autouse=True)
def isolated_process_table(monkeypatch):
    monkeypatch.setattr("hermes_cli.worker.runners.omo_processes._process_table", dict)


def frozen_case(tmp_path, monkeypatch, *, now=1700, last_entry=1000, report_time=950, state="running",
                streaming=True, background=None, stop="stop", obeys_abort=None):
    monkeypatch.setattr("hermes_cli.worker.runners.omo_settle.time.time", lambda: now)
    task = Task("OT-FROZEN", tmp_path)
    task.work.mkdir()
    task.path("started.1").write_text("900")
    if report_time is not None:
        report = task.work / "report.md"
        report.write_text("completed")
        os.utime(report, (report_time, report_time))
    task.path("settle.1.json").write_text(json.dumps({
        "start": 900, "quiet_since": None, "last_key": None, "errs": 0, "wakes": 0}))
    session = {"lifecycle": {"state": state, "waitingOn": {
        "background": DELIVERIES if background is None else background, "human": [], "children": 0}},
        "isStreaming": streaming, "pendingDialogs": [], "pendingQuestions": []}
    calls, obeys = [], [True] if obeys_abort is None else obeys_abort

    def call(argv, timeout):
        calls.append(argv[0])
        if argv[0] == "abort" and obeys[0]:
            session.update(lifecycle={"state": "cancelled", "waitingOn": {}}, isStreaming=False)
        if argv[0] == "state":
            return {"action": "state", "state": session}
        if argv[0] == "read":
            return {"action": "read", "entries": [{
                "timestamp": datetime.fromtimestamp(last_entry, UTC).isoformat(),
                "message": {"role": "assistant", "stopReason": stop,
                            "content": [{"type": "text", "text": "Finished"}]}}]}
        return {"action": argv[0]}

    settle = Settle(task, 1, "session-test", call, {"answered_grace_s": 10, "stall_grace_s": 600,
                                                     "abort_wait_s": 0})
    return settle, calls


def wait_result(settle):
    return json.loads(settle.task.path("wait.1.json").read_text())


def test_frozen_streaming_after_a_fresh_report_stops_the_session_and_ends_done(tmp_path, monkeypatch):
    settle, calls = frozen_case(tmp_path, monkeypatch)
    assert settle.step() == "done"
    assert calls.index("abort") < calls.index("state", calls.index("abort")) < calls.index("unload")
    result = wait_result(settle)
    assert (result["outcome"], result["via"], result["quiet_s"]) == ("frozen_done", "frozen streaming after report",
                                                                     700)


def test_frozen_done_whose_session_does_not_stop_holds_the_run_and_aborts_again(tmp_path, monkeypatch):
    obeys = [False]
    settle, calls = frozen_case(tmp_path, monkeypatch, obeys_abort=obeys)
    assert settle.step() == "running"
    assert "unload" not in calls
    assert not settle.task.path("wait.1.json").exists()
    obeys[0] = True
    assert Settle(settle.task, 1, settle.ref, settle.call, settle.limits).step() == "done"
    assert calls.count("abort") == 2 and "unload" in calls
    assert wait_result(settle)["via"] == "frozen streaming after report"


@pytest.mark.parametrize("changes", [{"report_time": None}, {"report_time": 800}, {"stop": "toolUse"}])
def test_frozen_streaming_without_a_finished_answer_and_fresh_report_stalls(tmp_path, monkeypatch, changes):
    settle, calls = frozen_case(tmp_path, monkeypatch, **changes)
    assert settle.step() == "stalled"
    assert wait_result(settle)["why"].startswith("frozen streaming")
    assert "abort" in calls


def test_streaming_session_that_keeps_writing_entries_keeps_running(tmp_path, monkeypatch):
    settle, calls = frozen_case(tmp_path, monkeypatch, last_entry=1650)
    assert settle.step() == "running"
    assert not settle.task.path("wait.1.json").exists()
    assert "abort" not in calls


def test_frozen_streaming_with_real_background_work_is_left_to_the_background_caps(tmp_path, monkeypatch):
    settle, _ = frozen_case(tmp_path, monkeypatch, background=[
        {"source": "senpi-codemode", "activeCount": 1, "items": [{"id": "job", "startedAt": "1970-01-01T00:25:00Z"}]}])
    assert settle.step() == "running"


def test_waiting_background_with_only_empty_deliveries_and_a_fresh_report_ends_done(tmp_path, monkeypatch):
    settle, _ = frozen_case(tmp_path, monkeypatch, now=1100, state="waiting_background", streaming=False)
    assert settle.step() == "done"


def test_end_notice_names_the_rule_that_ended_the_run(tmp_path):
    (tmp_path / "manifest.json").write_text(json.dumps({"run": 1, "wait": {
        "outcome": "frozen_done", "via": "frozen streaming after report"}}))
    job = {"id": "OT-FROZEN", "bot": "agent", "topic": "t", "runner": "omo", "delivery": "ok"}
    text = notice.body(job, "done", "1", tmp_path, {})
    assert "Ended by: frozen streaming after report" in text.splitlines()[1]
