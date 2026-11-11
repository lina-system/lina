"""A review request is judged as a handoff, not as the card's final completion (lina#460).

A goal-mode card whose own text says "closes only after the reviewer's approval" could never be
handed to that reviewer: both terminal-handoff gates (``tools.kanban_tools._goal_gate`` and the CLI
``hermes_cli.kanban._goal_mode_handoff_rejection``) gave the judge the whole card as the goal, and
the judge read the missing approval as unmet work (NousResearch/hermes-agent#98160).

The fix scopes only the review request to "ready for the reviewer" through one shared
``GoalContract`` (``hermes_cli.kanban_review_gate``); ``complete`` keeps judging the whole card.
Test names carry the number of the issue's Done-when test line (1)-(7). The judge model is faked:
it behaves like the real one did on this card (without the contract, the missing approval means
``continue``; with the review-handoff contract it judges readiness by the evidence).
"""

from __future__ import annotations

import json
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import pytest

from hermes_cli import kanban as kc
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc


def _goals():
    """``hermes_cli.goals`` as it is NOW: other tests in a full run re-import it (a few delete it from
    ``sys.modules``), so a module-level reference can be a stale object that ``patch`` does not hit."""
    import importlib
    return importlib.import_module("hermes_cli.goals")


def _review_contract():
    import importlib
    return importlib.import_module("hermes_cli.kanban_review_gate").review_handoff_contract()


def _is_contract(obj) -> bool:
    """A non-empty GoalContract. Duck-typed: ``tools.kanban_tools`` may hold the class of an older
    import of ``hermes_cli.goals`` after such a re-import, so ``isinstance`` against today's class lies."""
    return type(obj).__name__ == "GoalContract" and not obj.is_empty()


_CARD_BODY = (
    "Build the thing.\n\n## Done when\n- the work is implemented and tested\n"
    "- review: reviewer=alice approves (final completion only; asked after the review request)\n"
)
_FINISHED = "implemented and tested; fixes for review point 1-4 included"
_UNFINISHED = "no code yet, just started"
_APPROVED = "approved by alice after rerunning the tests"


def _contract_judge(seen):
    def fake_judge(goal, last_response, **kwargs):
        contract = kwargs.get("contract")
        seen.append(contract)
        if contract is None:
            if "approved by alice" in last_response:
                return ("done", "approval is in the evidence", False, None, False)
            return ("continue", "reviewer approval not obtained yet", False, None, False)
        if "no code yet" in last_response:
            return ("continue", "nothing to review yet", False, None, False)
        return ("done", "ready for the reviewer", False, None, False)
    return fake_judge


@pytest.fixture
def board(monkeypatch, tmp_path):
    """A real board in a temp HERMES_HOME with the judge faked on both surfaces; ``seen`` records the
    ``contract`` of every judge call in order."""
    from tools import kanban_tools as tools

    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setenv("HERMES_PROFILE", "builder")
    monkeypatch.delenv("HERMES_DELEGATED_CHILD_CONTEXT", raising=False)
    monkeypatch.delenv("HERMES_KANBAN_TASK", raising=False)
    monkeypatch.delenv("HERMES_KANBAN_RUN_ID", raising=False)
    (home / "profiles" / "reviewer").mkdir(parents=True)
    (home / "profiles" / "reviewer" / "config.yaml").write_text("{}\n")
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    seen: list = []
    judge = _contract_judge(seen)
    monkeypatch.setattr(tools, "_goal_judge_available", lambda: True)
    monkeypatch.setattr(tools, "judge_goal", judge)
    monkeypatch.setattr("agent.auxiliary_client.get_text_auxiliary_client", lambda purpose: (object(), "judge"))
    monkeypatch.setattr(_goals(), "judge_goal", judge)
    return SimpleNamespace(tools=tools, seen=seen, monkeypatch=monkeypatch)


def _running_card(b, *, follow_up=False):
    """A claimed goal-mode card; the worker env points at its run. Returns (card id, follow-up id)."""
    with kbc.connect() as conn:
        tid = kb.create_task(conn, title="Card", body=_CARD_BODY, assignee="builder", goal_mode=True)
        child = (kb.create_task(conn, title="Follow-up", body="starts after the parent", assignee="builder",
                                parents=[tid]) if follow_up else None)
        claimed = kb.claim_task(conn, tid, claimer="builder:1")
        assert claimed is not None
    b.monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    b.monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(claimed.current_run_id))
    return tid, child


def _status(tid):
    with kbc.connect() as conn:
        return kb.get_task(conn, tid).status


def _tool_request_review(b, summary):
    return json.loads(b.tools._handle_request_review({"summary": summary, "reviewer": "reviewer"}))


def _cli_request_review(tid, summary):
    return kc.run_slash(f"request-review {tid} --reviewer reviewer --summary '{summary}'")


def _in_review_as_reviewer(b, tid):
    """Hand the card over (finished work) and let the reviewer claim it."""
    assert _tool_request_review(b, _FINISHED)["ok"] is True
    with kbc.connect() as conn:
        review_run = kb.claim_review_task(conn, tid, claimer="reviewer:1")
        assert review_run is not None
    b.monkeypatch.setenv("HERMES_PROFILE", "reviewer")
    b.monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(review_run.current_run_id))
    b.seen.clear()


def test_1_review_request_with_finished_work_is_allowed_although_approval_is_pending(board):
    tid, _ = _running_card(board)
    ok = _tool_request_review(board, _FINISHED)
    assert ok["ok"] is True, ok
    assert _status(tid) == "review"

    tid, _ = _running_card(board)
    out = _cli_request_review(tid, _FINISHED)
    assert "rejected" not in out, out
    assert _status(tid) == "review"


def test_2_review_request_with_unfinished_work_is_refused(board):
    tid, _ = _running_card(board)
    refused = _tool_request_review(board, _UNFINISHED)
    assert "nothing to review yet" in refused["error"]
    assert _status(tid) == "running"

    tid, _ = _running_card(board)
    out = _cli_request_review(tid, _UNFINISHED)
    assert "rejected by judge: nothing to review yet" in out, out
    assert _status(tid) == "running"
    assert all(_is_contract(c) for c in board.seen)


def test_3_complete_before_reviewer_approval_is_refused(board):
    tid, _ = _running_card(board)
    _in_review_as_reviewer(board, tid)
    refused = json.loads(board.tools._handle_complete({"summary": "looked at it"}))
    assert "reviewer approval not obtained" in refused["error"]
    out = kc.run_slash(f"complete {tid} --summary 'looked at it'")
    assert "rejected by judge: reviewer approval not obtained yet" in out, out
    assert _status(tid) != "done"
    assert board.seen == [None, None]  # complete judges the whole card on both surfaces


def test_4_complete_with_approval_in_the_evidence_is_allowed(board):
    tid, _ = _running_card(board)
    _in_review_as_reviewer(board, tid)
    done = json.loads(board.tools._handle_complete({"summary": _APPROVED}))
    assert done["ok"] is True, done
    assert _status(tid) == "done"
    assert board.seen == [None]

    tid, _ = _running_card(board)
    board.monkeypatch.setenv("HERMES_PROFILE", "builder")
    _in_review_as_reviewer(board, tid)
    out = kc.run_slash(f"complete {tid} --summary '{_APPROVED}'")
    assert "rejected" not in out, out
    assert _status(tid) == "done"
    assert board.seen == [None]


def test_5_open_dependent_follow_up_card_does_not_block_the_parents_review_request(board):
    tid, follow_up = _running_card(board, follow_up=True)
    ok = _tool_request_review(board, _FINISHED)
    assert ok["ok"] is True, ok
    assert _status(tid) == "review"
    assert _status(follow_up) == "todo"

    tid, follow_up = _running_card(board, follow_up=True)
    out = _cli_request_review(tid, _FINISHED)
    assert "rejected" not in out, out
    assert _status(tid) == "review"
    assert _status(follow_up) == "todo"


def test_6_tool_and_cli_pass_the_same_review_scope_and_still_fail_open():
    from tools import kanban_tools as kt

    task = SimpleNamespace(id="t_card", title="Card", body=_CARD_BODY, goal_mode=True)
    seen: list = []
    with patch.object(kt, "_goal_judge_available", lambda: True), patch.object(kt, "judge_goal", _contract_judge(seen)), \
            patch("agent.auxiliary_client.get_text_auxiliary_client", return_value=(object(), "judge")), \
            patch.object(_goals(), "judge_goal", _contract_judge(seen)):
        kt._goal_gate("kanban_request_review", task, "t_card", _FINISHED)
        assert kc._goal_mode_handoff_rejection(task, _FINISHED, review=True) == ("done", None)
    tool_scope, cli_scope = seen
    assert _is_contract(tool_scope) and tool_scope == cli_scope == _review_contract()

    # An unreachable judge still lets the review handoff through on both surfaces.
    failed = ("continue", "judge error: BadRequestError", False, None, True)
    with patch.object(kt, "_goal_judge_available", lambda: True), patch.object(kt, "judge_goal", return_value=failed), \
            patch("agent.auxiliary_client.get_text_auxiliary_client", return_value=(object(), "judge")), \
            patch.object(_goals(), "judge_goal", return_value=failed):
        kt._goal_gate("kanban_request_review", task, "t_card", _FINISHED)  # no raise
        assert kc._goal_mode_handoff_rejection(task, _FINISHED, review=True) == ("done", None)


def _judge_prompts(*calls):
    prompts: list[str] = []

    def fake_call_llm(**kwargs):
        prompts.append(kwargs["messages"][-1]["content"])
        return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content='{"verdict": "done", "reason": "ok"}'))])

    with patch("agent.auxiliary_client.call_llm", side_effect=fake_call_llm):
        for kwargs in calls:
            _goals().judge_goal(last_response="evidence", **kwargs)
    return prompts


def test_7_review_scope_reaches_the_judge_prompt_and_survives_the_goal_truncation():
    review_prompt, complete_prompt = _judge_prompts(
        {"goal": f"Card\n\n{_CARD_BODY}", "contract": _review_contract()}, {"goal": f"Card\n\n{_CARD_BODY}"})
    assert "Completion contract (the authoritative definition of done)" in review_prompt
    assert "Hand the finished work to a reviewer" in review_prompt
    assert "do not withhold DONE" in review_prompt
    assert _CARD_BODY.strip() in review_prompt          # the card is still the goal text
    assert "Completion contract" not in complete_prompt  # complete: template and text unchanged
    assert "Hand the finished work to a reviewer" not in complete_prompt

    # judge_goal truncates the goal to 2000 chars and the contract block to 2500: a long card body must
    # not push the review scope out of the prompt, and the scope itself must fit its budget.
    block = _review_contract().render_block()
    assert len(block) < 2500
    (long_prompt,) = _judge_prompts({"goal": "Card\n\n" + "x" * 6000, "contract": _review_contract()})
    assert block in long_prompt
