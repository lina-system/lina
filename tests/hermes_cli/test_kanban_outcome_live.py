"""Round-three outcome regressions: one act, current state, and durable redaction."""

import json
from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb, kanban_outcomes as ko
from hermes_cli.kanban_db_connect import connect_closing


@pytest.fixture
def board(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for key in ("HERMES_KANBAN_TASK", "HERMES_KANBAN_RUN_ID", "HERMES_KANBAN_DB"):
        monkeypatch.delenv(key, raising=False)
    clock = [1800000000]
    monkeypatch.setattr(kb.time, "time", lambda: clock[0])
    kb.init_db()
    with connect_closing() as conn:
        yield conn, clock


def blocked_card(conn, clock):
    tid = kb.create_task(conn, title="work", assignee="lina", goal_mode=True)
    kb.claim_task(conn, tid)
    clock[0] += 10
    kb.block_task(conn, tid, reason="waiting")
    return tid


@pytest.mark.parametrize("actor,kind,answers", [("owner", "answer", 1), ("sion", "unblock", 0)])
def test_answer_and_matching_unblock_are_one_touch(board, actor, kind, answers):
    conn, clock = board
    tid = blocked_card(conn, clock)
    clock[0] += 10
    text = "approved " + "x" * 230 + "\nextra detail"
    kb.add_comment(conn, tid, actor, text, source={"surface": "app"})
    kb.unblock_task(conn, tid, by=actor, reason=text)
    row = ko.compute_outcome(conn, tid, include_active=True)
    assert [(t["by"], t["kind"], t["source"]) for t in row["touches"]] == [
        (actor, kind, {"surface": "app"})]
    assert row["owner_answers"] == answers
    assert row["stops"][0]["resumed_by"] == actor


def test_standalone_owner_unblock_does_not_count_as_an_answer(board):
    conn, clock = board
    tid = blocked_card(conn, clock)
    clock[0] += 10
    kb.unblock_task(conn, tid, by="owner", reason="manual restart")
    row = ko.compute_outcome(conn, tid, include_active=True)
    assert [t["kind"] for t in row["touches"]] == ["unblock"]
    assert row["owner_answers"] == 0


@pytest.mark.parametrize("resume", [True, False], ids=["running", "terminal-new-event"])
def test_goal_recomputes_stale_rows_and_preserves_measured_sizes(board, resume):
    conn, clock = board
    tid = blocked_card(conn, clock)
    goal = kb.create_task(conn, title="goal", parents=[tid])
    sizes = {"todo": {"actual_size": "L", "files": 10, "lines": 900}}
    missed = [{"todo": "todo", "planned_size": "S", "actual_size": "L"}]
    conn.execute("UPDATE work_card_outcomes SET actual_size='L',todo_sizes=?,size_missed=? WHERE task_id=?",
                 (json.dumps(sizes), json.dumps(missed), tid))
    conn.commit()
    clock[0] += 10
    if resume:
        kb.unblock_task(conn, tid, by="sion")
        kb.claim_task(conn, tid)
    kb.add_comment(conn, tid, "owner", "new answer", source={"surface": "app"})
    before = conn.total_changes
    row = ko.goal_outcome(conn, goal, now=clock[0] + 10)["rows"][0]
    assert row["status"] == ("running" if resume else "blocked")
    assert row["owner_answers"] == 1
    assert (row["actual_size"], row["todo_sizes"], row["size_missed"]) == ("L", sizes, missed)
    assert conn.total_changes == before


def test_resumed_lead_time_continues_until_now(board):
    conn, clock = board
    start = clock[0]
    tid = blocked_card(conn, clock)
    assert ko.compute_outcome(conn, tid, now=start + 100)["lead_seconds"] == 10
    clock[0] += 10
    kb.unblock_task(conn, tid)
    kb.claim_task(conn, tid)
    assert ko.compute_outcome(conn, tid, now=start + 100, include_active=True)["lead_seconds"] == 100
    clock[0] = start + 120
    kb.complete_task(conn, tid, result="done")
    assert ko.compute_outcome(conn, tid, now=start + 200)["lead_seconds"] == 120


@pytest.mark.parametrize("shape", ["prefix", "env", "json"])
def test_stored_stop_reason_retains_no_secret_material(board, shape):
    conn, _clock = board
    material = "Q7v9M2r8N4p6Z1x3C5b0L7s9T2u4W6y8"
    values = {"prefix": "ghp_" + material + "AbCd", "env": material, "json": material}
    reason = {"prefix": f"blocked by {values[shape]}", "env": f"API_KEY={values[shape]}",
              "json": json.dumps({"api_key": values[shape]})}[shape]
    tid = kb.create_task(conn, title="secret", assignee="lina", goal_mode=True)
    kb.claim_task(conn, tid)
    kb.block_task(conn, tid, reason=reason)
    stored = conn.execute("SELECT stops FROM work_card_outcomes WHERE task_id=?", (tid,)).fetchone()[0]
    line = json.loads(stored)[0]["reason"]
    expected = {"prefix": "blocked by «redacted:ghp_…»", "env": "API_KEY=«redacted-secret»",
                "json": '{"api_key": "«redacted-secret»"}'}[shape]
    assert line == expected
    assert material[:6] not in stored and material[-4:] not in stored
