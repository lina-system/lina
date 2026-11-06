"""lina#295: a card is finished only when every run it started has a verdict.

Runs are started and ended on a real temporary board through ``kanban_link`` (the hub's own
writers); verdicts are the hub's job records under the temporary LINA root. The tick is the real
``wake_waiting_tasks`` with a moved clock.
"""
from __future__ import annotations

import json
import time
from pathlib import Path

import pytest

from hermes_cli import goals
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.kanban_db_wait import wake_waiting_tasks
from hermes_cli.worker import kanban_link, settings

RUN = "OT-AGT1107120000"
OTHER = "OT-AGT1107120500"


@pytest.fixture
def conn(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for var in ("HERMES_KANBAN_DB", "HERMES_KANBAN_WORKSPACES_ROOT", "HERMES_KANBAN_HOME", "HERMES_KANBAN_BOARD",
                "HERMES_KANBAN_TASK", "LINA_HOME"):
        monkeypatch.delenv(var, raising=False)
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    with kbc.connect_closing() as c:
        yield c


def _hub() -> Path:
    return settings.hub_dir(settings.root())


def _job(run_id: str, card: str, **fields) -> None:
    jobs = _hub() / "jobs"
    jobs.mkdir(parents=True, exist_ok=True)
    job = {"id": run_id, "bot": "agent-a", "state": "done", "verdict": None, "card": {"task": card, "board": None},
           "created": time.time(), **fields}
    (jobs / f"{run_id}.json").write_text(json.dumps(job), encoding="utf-8")


def _start(card: str, run_id: str = RUN) -> None:
    _job(run_id, card, state="running")
    kanban_link.record_run({"task": card, "board": None}, {"id": run_id, "runner": "fakecli"}, {"kind": "lina"})


def _end(card: str, run_id: str = RUN, state: str = "done") -> None:
    _job(run_id, card, state=state)
    kanban_link.record_end({"task": card, "board": None}, {"id": run_id, "runner": "fakecli"}, state, "1",
                           f"run {run_id} ended")


def _verdict(card: str, run_id: str = RUN, verdict: str = "accepted") -> None:
    _job(run_id, card, verdict=verdict)


def _events(conn, card: str, kind: str) -> list:
    return [e for e in kb.list_events(conn, card) if e.kind == kind]


def _notices() -> list[Path]:
    return sorted((_hub() / "notices").glob("*.md")) if (_hub() / "notices").is_dir() else []


def test_finishing_a_card_is_refused_while_a_run_it_started_has_no_verdict(conn):
    card = kb.create_task(conn, title="card", assignee="agent-a")
    _start(card)
    _end(card)
    with pytest.raises(ValueError, match=rf"{RUN} \(done\)"):
        kb.complete_task(conn, card, result="shipped")
    with pytest.raises(ValueError, match=rf"{RUN} \(done\)"):
        kb.request_review(conn, card, summary="shipped")
    assert kb.get_task(conn, card).status == "ready"

    _verdict(card)
    assert kb.complete_task(conn, card, result="shipped")


def test_a_cancelled_run_counts_as_judged_and_a_running_one_does_not(conn):
    card = kb.create_task(conn, title="card")
    _start(card)
    _start(card, OTHER)
    _end(card, OTHER, state="cancelled")
    with pytest.raises(ValueError) as refused:
        kb.complete_task(conn, card, result="shipped")
    assert RUN in str(refused.value) and "(running)" in str(refused.value) and OTHER not in str(refused.value)


def test_the_operator_override_closes_and_is_recorded_on_the_card(conn):
    card = kb.create_task(conn, title="card")
    _start(card)
    assert kb.complete_task(conn, card, result="closed by hand", allow_unjudged=True)
    allowed = _events(conn, card, "unjudged_runs_allowed")
    assert allowed and allowed[0].payload["runs"] == [{"run_id": RUN, "state": "running"}]


def test_a_goal_judge_done_is_a_continuation_while_runs_have_no_verdict(conn, monkeypatch):
    card = kb.create_task(conn, title="goal card", assignee="agent-a")
    _start(card)
    _end(card)
    monkeypatch.setattr(goals, "judge_goal", lambda *a, **k: ("done", "looks complete", False, None, False))
    prompts: list[str] = []
    statuses = iter(["running", "done"])
    goals.run_kanban_goal_loop(task_id=card, goal_text="ship", run_turn=lambda p: prompts.append(p) or "ok",
                               task_status_fn=lambda: next(statuses), block_fn=lambda m: None)
    assert len(prompts) == 1 and RUN in prompts[0] and "Not done" in prompts[0]


def test_a_run_end_lets_a_card_behind_an_open_parent_judge_it_then_wait_again(conn):
    parent = kb.create_task(conn, title="parent")
    card = kb.create_task(conn, title="child", assignee="agent-a", parents=[parent])
    assert kb.get_task(conn, card).status == "todo"
    _start(card)
    _end(card)

    assert kb.get_task(conn, card).status == "ready"
    claimed = kb.claim_task(conn, card, claimer="agent-a")
    assert claimed is not None, "the woken card must be claimable past its open parent"
    with pytest.raises(ValueError, match=RUN):
        from hermes_cli.kanban_db_verdicts import return_to_wait
        return_to_wait(conn, card, expected_run_id=claimed.current_run_id)

    _verdict(card)
    assert return_to_wait(conn, card, expected_run_id=claimed.current_run_id)
    assert kb.get_task(conn, card).status == "todo"
    assert kb.complete_task(conn, parent, result="parent done")
    assert kb.get_task(conn, card).status == "ready"


def test_a_woken_card_judged_elsewhere_goes_back_to_its_schedule_on_the_tick(conn):
    card = kb.create_task(conn, title="paused", assignee="agent-a")
    _start(card)
    assert kb.schedule_task(conn, card, reason="until 2099-01-01", wait_for={"kind": "until", "at": 4070908800})
    _end(card)
    assert kb.get_task(conn, card).status == "ready"
    wake_waiting_tasks(conn)
    assert kb.get_task(conn, card).status == "ready"

    _verdict(card, verdict="rejected")
    wake_waiting_tasks(conn)
    assert kb.get_task(conn, card).status == "scheduled"
    from hermes_cli.kanban_db_wait import recorded_wait
    assert recorded_wait(conn, card, "scheduled") == {"kind": "until", "at": 4070908800}


def test_an_unjudged_run_is_raised_once_on_its_card_after_an_hour_never_to_a_session(conn):
    card = kb.create_task(conn, title="card", assignee="agent-a")
    _start(card)
    _end(card)
    wake_waiting_tasks(conn, now=int(time.time()) + 600)
    assert not _events(conn, card, "unjudged_run_raised")
    for _ in range(2):
        wake_waiting_tasks(conn, now=int(time.time()) + 3700)
    raised = _events(conn, card, "unjudged_run_raised")
    assert len(raised) == 1 and raised[0].payload["run_id"] == RUN
    assert any(RUN in c.body and c.author == "kanban-dispatcher" for c in kb.list_comments(conn, card))
    assert not _notices()


def test_an_unjudged_run_of_a_closed_card_goes_once_to_the_starting_agents_main_session(conn):
    card = kb.create_task(conn, title="card", assignee="agent-a")
    _start(card)
    _end(card)
    assert kb.complete_task(conn, card, result="closed", allow_unjudged=True)
    for _ in range(2):
        wake_waiting_tasks(conn, now=int(time.time()) + 3700)
    notices = _notices()
    assert len(notices) == 1 and notices[0].name.startswith(f"{RUN}-unjudged-")
    assert RUN in notices[0].read_text(encoding="utf-8")


def test_a_pause_with_no_reopen_condition_is_raised_once_after_a_day(conn):
    open_card = kb.create_task(conn, title="open pause", assignee="agent-a")
    named = kb.create_task(conn, title="named pause", assignee="agent-a")
    assert kb.schedule_task(conn, open_card, reason="waiting for things to settle")
    assert kb.schedule_task(conn, named, reason=f"waiting for {RUN}")
    wake_waiting_tasks(conn, now=int(time.time()) + 3600)
    assert not _notices()
    for _ in range(2):
        wake_waiting_tasks(conn, now=int(time.time()) + 86400 + 60)
    notices = _notices()
    assert [n.name.split("-pause-")[0] for n in notices] == [open_card]
    assert _events(conn, open_card, "open_pause_raised")[0].payload["agent"] == "agent-a"

