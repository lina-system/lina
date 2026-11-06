"""Work card outcome rows (lina#111): written at done/blocked from the board's own records."""

from __future__ import annotations

import json
import logging
import sqlite3
import time
from pathlib import Path

import pytest

from hermes_cli import kanban as kc
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli import kanban_db_dispatch as kbd
from hermes_cli import kanban_outcomes as ko

DONE_BODY = """## Goal
ship outcomes

## To-dos
- lina-system/lina#5
- lina-system/lina#6
- [x] write the docs
- [ ] record a demo

## Verification
- [x] not a to-do
"""


@pytest.fixture
def kanban_home(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("HERMES_PROFILE", "lina")
    monkeypatch.delenv("HERMES_SESSION_ID", raising=False)
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    (home / "profiles" / "lina").mkdir(parents=True)
    (home / "profiles" / "lina" / "config.yaml").write_text("{}\n")
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    return home


@pytest.fixture
def gh_calls(monkeypatch):
    calls = []
    closed = {("lina-system/lina", 5): True, ("lina-system/lina", 6): False}

    def fake(repo, n, **_kw):
        calls.append((repo, n))
        return closed.get((repo, n))

    monkeypatch.setattr(ko, "gh_issue_closed", fake)
    return calls


def _comment(conn, tid, author, body, at):
    conn.execute("INSERT INTO task_comments (task_id, author, body, created_at) VALUES (?, ?, ?, ?)",
                 (tid, author, body, at))


def _gate(conn, tid, command, exit_code):
    with kb.write_txn(conn):
        kb._append_event(conn, tid, "goal_gate", {"command": command, "exit_code": exit_code,
                                                 "passed": exit_code == 0, "output_tail": ""})


def _claimed_card(conn, title, body, *, started_ago=3600, created_ago=7200):
    tid = kb.create_task(conn, title=title, body=body, assignee="lina", created_by="lina",
                         goal_mode=True)
    kb.claim_task(conn, tid)
    run_id = kb._current_run_id(conn, tid)
    now = int(time.time())
    conn.execute("UPDATE tasks SET created_at = ? WHERE id = ?", (now - created_ago, tid))
    conn.execute("UPDATE task_runs SET started_at = ? WHERE id = ?", (now - started_ago, run_id))
    conn.commit()
    return tid, run_id, now


def _outcome_rows(conn, tid):
    return conn.execute("SELECT * FROM work_card_outcomes WHERE task_id = ?", (tid,)).fetchall()


def _seed_done_card(conn):
    tid, run_id, now = _claimed_card(conn, "feat(kanban): ship outcomes", DONE_BODY)
    in_run = now - 1800
    _comment(conn, tid, "lina", "started OT-SIO1103015630 for lina-system/lina#5", in_run)
    _comment(conn, tid, "lina", "quick-issue job qi-20261102-7 on #6", in_run)
    _comment(conn, tid, "lina", "retry OT-SIO1103020000 on lina-system/lina#5", in_run)
    _comment(conn, tid, "lina", "escalate: omo takes #6 next", in_run)
    _comment(conn, tid, "lina", "added-by: work-session\n- [ ] extra check", in_run)
    _comment(conn, tid, "user", "yes, go ahead", in_run)
    _comment(conn, tid, "lina", "main session: also look at the docs", now - 5000)
    _comment(conn, tid, "lina", "before registration", now - 9000)
    conn.commit()
    _gate(conn, tid, "kanban gate prs", 1)
    _gate(conn, tid, "kanban gate prs", 75)
    _gate(conn, tid, "kanban gate prs", 0)
    _gate(conn, tid, "kanban gate dev lina-system/lina", 1)
    _gate(conn, tid, "kanban gate dev lina-system/lina", 0)
    return tid, run_id, now


def test_done_card_gets_one_row_from_board_records(kanban_home, gh_calls):
    with kbc.connect_closing() as conn:
        tid, run_id, _now = _seed_done_card(conn)
        assert kb.complete_task(conn, tid, result="shipped", metadata={"turns_used": 7},
                                expected_run_id=run_id)
        rows = _outcome_rows(conn, tid)
    assert len(rows) == 1
    row = dict(rows[0])
    assert {k: row[k] for k in ("assignee", "kind", "status", "planned_todos", "closed_todos",
                                "added_todos", "worker_run_count", "escalations", "turns_used",
                                "gate_failures", "owner_answers", "main_session_touches",
                                "dev_red_runs")} == {
        "assignee": "lina", "kind": "feat", "status": "done", "planned_todos": 4,
        "closed_todos": None, "added_todos": 1, "worker_run_count": 3, "escalations": 1,
        "turns_used": 7, "gate_failures": 2, "owner_answers": 1, "main_session_touches": 1,
        "dev_red_runs": 1,
    }
    assert 3600 <= row["elapsed_seconds"] <= 3605
    assert gh_calls == []  # the transition never asks GitHub
    assert json.loads(row["worker_runs"]) == {
        "lina-system/lina#5": [{"runner": "omo", "id": "OT-SIO1103015630"},
                               {"runner": "omo", "id": "OT-SIO1103020000"}],
        "lina-system/lina#6": [{"runner": "quick-issue", "id": "qi-20261102-7"}],
    }


def test_blocked_card_gets_one_row_and_a_later_done_replaces_it(kanban_home):
    with kbc.connect_closing() as conn:
        tid, run_id, _now = _claimed_card(conn, "docs update", "## To-dos\n- [ ] a\n- [x] b\n")
        assert kb.block_task(conn, tid, reason="stuck", expected_run_id=run_id)
        rows = _outcome_rows(conn, tid)
        assert len(rows) == 1
        blocked = dict(rows[0])
        assert kb.complete_task(conn, tid, result="unstuck and done")
        rows = _outcome_rows(conn, tid)
    assert {k: blocked[k] for k in ("kind", "status", "planned_todos", "closed_todos",
                                    "gate_failures", "dev_red_runs", "turns_used")} == {
        "kind": "general", "status": "blocked", "planned_todos": 2, "closed_todos": 1,
        "gate_failures": 0, "dev_red_runs": None, "turns_used": 1,
    }
    assert 3600 <= blocked["elapsed_seconds"] <= 3605
    assert [r["status"] for r in rows] == ["done"]


def test_refresh_fills_github_todos_later(kanban_home, gh_calls):
    with kbc.connect_closing() as conn:
        tid, run_id, _now = _claimed_card(conn, "feat: x", DONE_BODY)
        assert kb.complete_task(conn, tid, result="ok", expected_run_id=run_id)
        plain = kb.create_task(conn, title="feat: plain card", body=DONE_BODY, assignee="lina")
        assert kb.complete_task(conn, plain, result="ok")
        assert _outcome_rows(conn, plain) == []
    assert gh_calls == []
    data = json.loads(kc.run_slash("outcomes --refresh --json"))
    assert data["refreshed"] == {"filled": 1, "still_null": 0}
    assert data["rows"][0]["closed_todos"] == 2
    assert sorted(gh_calls) == [("lina-system/lina", 5), ("lina-system/lina", 6)]


def test_refresh_leaves_null_when_gh_is_unavailable(kanban_home, monkeypatch):
    monkeypatch.setattr(ko, "gh_issue_closed", lambda repo, n, **_kw: None)
    with kbc.connect_closing() as conn:
        tid, run_id, _now = _claimed_card(conn, "fix: x", "## To-dos\n- lina-system/lina#9\n")
        assert kb.complete_task(conn, tid, result="ok", expected_run_id=run_id)
    data = json.loads(kc.run_slash("outcomes --refresh --json"))
    assert data["refreshed"] == {"filled": 0, "still_null": 1}
    assert data["rows"][0]["closed_todos"] is None


def test_breaker_block_writes_the_row(kanban_home, monkeypatch):
    from hermes_cli import profiles

    monkeypatch.setattr(profiles, "profile_exists", lambda name: name == "lina")

    def spawn_fails(*_a, **_kw):
        raise RuntimeError("spawn exploded")

    with kbc.connect_closing() as conn:
        tid = kb.create_task(conn, title="feat: breaker", body="## To-dos\n- [x] a\n- [ ] b\n",
                             assignee="lina", goal_mode=True)
        result = kbd.dispatch_once(conn, spawn_fn=spawn_fails, failure_limit=1)
        assert result.auto_blocked == [tid]
        assert kb.get_task(conn, tid).status == "blocked"
        rows = _outcome_rows(conn, tid)
    assert [(r["status"], r["planned_todos"], r["closed_todos"]) for r in rows] == [("blocked", 2, 1)]


def test_failing_writer_never_blocks_the_transition(kanban_home, monkeypatch, caplog):
    def boom(*_a, **_kw):
        raise RuntimeError("writer exploded")

    monkeypatch.setattr(ko, "compute_outcome", boom)
    with kbc.connect_closing() as conn:
        done_id, done_run, _ = _claimed_card(conn, "feat: a", "## To-dos\n- [x] a\n")
        blocked_id, blocked_run, _ = _claimed_card(conn, "feat: b", "## To-dos\n- [ ] b\n")
        with caplog.at_level(logging.WARNING, logger="hermes_cli.kanban_outcomes"):
            assert kb.complete_task(conn, done_id, result="ok", expected_run_id=done_run)
            assert kb.block_task(conn, blocked_id, reason="stuck", expected_run_id=blocked_run)
        assert kb.get_task(conn, done_id).status == "done"
        assert kb.get_task(conn, blocked_id).status == "blocked"
        assert conn.execute("SELECT COUNT(*) FROM work_card_outcomes").fetchone()[0] == 0
    assert "could not record the outcome" in caplog.text


def test_outcomes_command_filters_and_summarizes(kanban_home, gh_calls):
    with kbc.connect_closing() as conn:
        tid, run_id, _now = _seed_done_card(conn)
        assert kb.complete_task(conn, tid, result="shipped", expected_run_id=run_id)
        other, other_run, _ = _claimed_card(conn, "docs: guide", "## To-dos\n- [ ] a\n")
        assert kb.block_task(conn, other, reason="stuck", expected_run_id=other_run)

    data = json.loads(kc.run_slash("outcomes --json"))
    assert [r["task_id"] for r in data["rows"]] == [other, tid]
    feat = json.loads(kc.run_slash("outcomes --kind feat --json"))
    assert [r["task_id"] for r in feat["rows"]] == [tid]
    assert feat["summary"] == [{
        "agent": "lina", "kind": "feat", "cards": 1, "done": 1,
        "first_try": {"omo": {"first_try": 0, "todos": 1},
                      "quick-issue": {"first_try": 0, "todos": 1}},
        "median_hours": 1.0, "mean_gate_failures": 2,
    }]
    assert json.loads(kc.run_slash("outcomes --agent nobody --json")) == {"rows": [], "summary": []}
    assert [r["task_id"] for r in json.loads(kc.run_slash("outcomes --last 1 --json"))["rows"]] == [other]
    text = kc.run_slash("outcomes")
    assert "lina / docs: 1 cards, done 0/1" in text
    assert "lina / feat: 1 cards, done 1/1, first-try omo 0/1 quick-issue 0/1, median 1.0h" in text


def test_old_board_opens_unchanged_and_gains_the_table(tmp_path, monkeypatch):
    db = tmp_path / "legacy.db"
    legacy = sqlite3.connect(db)
    legacy.executescript(kb.SCHEMA_SQL)
    legacy.execute("INSERT INTO tasks (id, title, status, created_at) VALUES ('t_old', 'old', 'done', 1)")
    legacy.commit()
    legacy.close()
    kb._INITIALIZED_PATHS.clear()
    with kbc.connect_closing(db) as conn:
        assert kb.get_task(conn, "t_old").title == "old"
        assert conn.execute("SELECT COUNT(*) FROM work_card_outcomes").fetchone()[0] == 0


def _card_with_comments(conn, title, body, comments):
    tid, run_id, now = _claimed_card(conn, title, body)
    for text in comments:
        _comment(conn, tid, "lina", text, now - 1800)
    conn.commit()
    assert kb.complete_task(conn, tid, result="ok", expected_run_id=run_id)
    return tid


def test_first_try_needs_completion_evidence(kanban_home, gh_calls):
    body = "## To-dos\n- [x] write docs\n- [ ] record a demo\n- lina-system/lina#5\n"
    with kbc.connect_closing() as conn:
        _card_with_comments(conn, "feat: evidence", body, [
            "OT-SIO1103011111 on write docs", "OT-SIO1103022222 on record a demo",
            "OT-SIO1103033333 on lina-system/lina#5"])
    before = json.loads(kc.run_slash("outcomes --json"))["summary"][0]["first_try"]
    after = json.loads(kc.run_slash("outcomes --refresh --json"))["summary"][0]["first_try"]
    assert before == {"omo": {"first_try": 1, "todos": 3}}
    assert after == {"omo": {"first_try": 2, "todos": 3}}


def test_worker_runs_need_a_complete_issue_reference(kanban_home):
    body = "## To-dos\n- acme/repo#5\n- acme/repo#50\n- acme/repo#12\n- acme/repo#123\n"
    with kbc.connect_closing() as conn:
        tid = _card_with_comments(conn, "feat: refs", body, [
            "OT-SIO1103044444 for acme/repo#50", "OT-SIO1103055555 for acme/repo#123",
            "OT-SIO1103066666 picks up #12.", "OT-SIO1103077777 on other/repo#5"])
        runs = json.loads(_outcome_rows(conn, tid)[0]["worker_runs"])
    assert {todo: [r["id"] for r in rs] for todo, rs in runs.items()} == {
        "acme/repo#50": ["OT-SIO1103044444"], "acme/repo#123": ["OT-SIO1103055555"],
        "acme/repo#12": ["OT-SIO1103066666"], ko.CARD_SCOPE: ["OT-SIO1103077777"]}


def test_quick_issue_end_comments_each_count_one_run_on_their_issue(kanban_home):
    body = "## To-dos\n- lina-system/lina#5\n- lina-system/lina#6\n"
    with kbc.connect_closing() as conn:
        tid = _card_with_comments(conn, "fix: quick-issue", body, [
            "[quick-issue] lina-system/lina#5 → PR #8 (lina, 12분, 3 파일)",
            "[quick-issue] 이어서 lina-system/lina#5 → PR #8에 커밋 하나 더 (lina, 4분, 1 파일)",
        ])
        row = dict(_outcome_rows(conn, tid)[0])
    assert json.loads(row["worker_runs"]) == {
        "lina-system/lina#5": [{"runner": "quick-issue", "id": "lina-system/lina#5"},
                               {"runner": "quick-issue", "id": "lina-system/lina#5"}],
    }
    assert (row["worker_run_count"], row["planned_todos"]) == (2, 2)


def test_quick_issue_run_on_another_issue_belongs_to_the_card_scope(kanban_home):
    body = "## To-dos\n- acme/repo#8\n"
    with kbc.connect_closing() as conn:
        tid = _card_with_comments(conn, "fix: quick-issue", body, [
            "[quick-issue] acme/repo#5 → PR #8 (lina, 12분, 3 파일)"])
        row = dict(_outcome_rows(conn, tid)[0])
    assert json.loads(row["worker_runs"]) == {
        ko.CARD_SCOPE: [{"runner": "quick-issue", "id": "acme/repo#5"}]}
    assert row["worker_run_count"] == 1


def test_refresh_runs_gh_as_the_assignee_profile(kanban_home, monkeypatch):
    seen = []

    class Done:
        returncode, stdout = 0, "CLOSED\n"

    def fake_run(command, **kw):
        seen.append((command, kw.get("env")))
        return Done()

    monkeypatch.setattr(ko.subprocess, "run", fake_run)
    with kbc.connect_closing() as conn:
        _card_with_comments(conn, "feat: login", "## To-dos\n- acme/repo#7\n", [])
    assert json.loads(kc.run_slash("outcomes --refresh --json"))["refreshed"] == {
        "filled": 1, "still_null": 0}
    command, env = seen[0]
    assert command[:3] == ["gh", "issue", "view"]
    assert env["GH_CONFIG_DIR"] == str(kanban_home / "profiles" / "lina" / "gh")


def test_gh_missing_leaves_the_state_unknown(kanban_home, monkeypatch):
    def no_gh(*_a, **_kw):
        raise FileNotFoundError("gh")

    monkeypatch.setattr(ko.subprocess, "run", no_gh)
    assert ko.gh_issue_closed("acme/repo", 7) is None


def test_learning_turns_use_events_and_started_runs(kanban_home):
    with kbc.connect_closing() as conn:
        tid, run_id, now = _claimed_card(conn, "turns", "Size: M\nKind: research")
        with kb.write_txn(conn):
            for turn in (2, 3):
                kb._append_event(conn, tid, "goal_turn", {"turn": turn}, run_id=run_id)
        kb.complete_task(conn, tid, result="done", expected_run_id=run_id)
        row = ko.compute_outcome(conn, tid, now=now)
    assert row["turns_used"] == 3
    assert row["turn_budget"] > 0
    assert (row["kind"], row["planned_size"]) == ("research", "M")


def test_learning_sources_override_run_windows_and_exclude_tools(kanban_home):
    with kbc.connect_closing() as conn:
        tid, run_id, now = _claimed_card(conn, "feat: hands", "")
        for author, text, source, own_run in (
            ("lina", "outside session", {"surface": "cli", "session": "chat"}, None),
            ("lina", "own worker", None, run_id),
            ("lina", "another card", {"card": "t_other", "run": 900}, None),
            ("owner", "approved", {"surface": "app"}, None),
            ("lina-worker", "notification", {"surface": "cli"}, None),
        ):
            kb.add_comment(conn, tid, author, text, source=source, run_id=own_run)
        kb.complete_task(conn, tid, result="done", expected_run_id=run_id)
        row = ko.compute_outcome(conn, tid, now=now)
    assert (row["main_session_touches"], row["owner_answers"]) == (2, 1)
    assert [t["line"] for t in row["touches"]] == ["outside session", "another card", "approved"]


def test_learning_review_keeps_implementer_and_separates_reviewer(kanban_home):
    with kbc.connect_closing() as conn:
        tid, run_id, now = _claimed_card(conn, "docs: review", "")
        assert kb.request_review(conn, tid, reviewer="rumi", expected_run_id=run_id)
        assert kb.claim_review_task(conn, tid)
        review_run = kb._current_run_id(conn, tid)
        kb.add_comment(conn, tid, "rumi", "review evidence")
        kb.complete_task(conn, tid, result="accepted", expected_run_id=review_run)
        row = ko.compute_outcome(conn, tid, now=now)
    assert row["assignee"] == "lina"
    assert row["touches"] == []
    assert [(r["reviewer"], r["verdict"]) for r in row["reviews"]] == [("rumi", "completed")]


def test_learning_stops_pair_next_resume_and_union_duration(kanban_home):
    with kbc.connect_closing() as conn:
        tid, run_id, now = _claimed_card(conn, "stop", "")
        with kb.write_txn(conn):
            for kind, at, payload in (
                ("timed_out", now - 300, {"reason": "timeout\nprivate detail"}),
                ("gave_up", now - 290, {"reason": "breaker"}),
                ("unblocked", now - 200, {"by": "rumi"}),
                ("goal_gate", now - 180, {"exit_code": 75}),
                ("goal_gate", now - 120, {"exit_code": 75}),
            ):
                kb._append_event(conn, tid, kind, payload, run_id=run_id)
                conn.execute("UPDATE task_events SET created_at=? WHERE id=last_insert_rowid()", (at,))
        kb.block_task(conn, tid, reason="waiting", expected_run_id=run_id)
        row = ko.compute_outcome(conn, tid, now=now + 50)
    assert [(s["seconds"], s["resumed_by"]) for s in row["stops"][:2]] == [(100, "rumi"), (90, "rumi")]
    assert row["stops"][0]["reason"] == "timeout"
    assert row["stops"][-1]["until"] is None
    assert row["stopped_seconds"] == 150
    assert (row["waiting_gate_checks"], row["waiting_gate_seconds"]) == (2, 60)
    assert row["lead_seconds"] >= 7200


def test_learning_replans_and_worker_events_are_deduplicated(kanban_home):
    with kbc.connect_closing() as conn:
        tid, run_id, now = _claimed_card(conn, "feat: plan", "## To-dos\n- [ ] docs")
        kb.add_comment(conn, tid, "lina", "added-by: work-session\n- [ ] extra")
        kb.add_comment(conn, tid, "lina", "escalate: docs -> omo - scope grew")
        kb.add_comment(conn, tid, "lina-worker", "lina worker run job-123 on docs")
        with kb.write_txn(conn):
            for kind, payload in (
                ("edited", {"fields": ["body"], "by": "rumi", "reason": "scope\nignored"}),
                ("decomposed", {"by": "lina", "reason": "split"}),
                ("linked", {"parent": "t_other", "child": tid, "by": "rumi", "reason": "dependency"}),
                ("worker_run", {"run_id": "job-123", "runner": "lina-worker"}),
                ("worker_run", {"run_id": "job-456", "runner": "omo"}),
            ):
                kb._append_event(conn, tid, kind, payload)
        kb.complete_task(conn, tid, result="ok", expected_run_id=run_id)
        row = ko.compute_outcome(conn, tid, now=now)
    assert {r["kind"] for r in row["replans"]} == {"edit", "added", "escalation", "decomposed", "link"}
    assert next(r["reason"] for r in row["replans"] if r["kind"] == "edit") == "scope"
    assert row["worker_run_count"] == 2


def test_learning_size_refresh_reads_closed_issue_and_pr(kanban_home, monkeypatch):
    calls = []

    def fake_run(command, **kwargs):
        calls.append((command, kwargs["env"]))
        from types import SimpleNamespace
        if "pr" in command:
            data = {"additions": 160, "deletions": 10, "changedFiles": 2}
        elif "closedByPullRequestsReferences,labels,body" in command:
            data = {"body": "Size: S", "labels": [], "closedByPullRequestsReferences": [
                {"url": "https://github.com/acme/repo/pull/8"}]}
        else:
            return SimpleNamespace(returncode=0, stdout="CLOSED\n")
        return SimpleNamespace(returncode=0, stdout=json.dumps(data))

    monkeypatch.setattr(ko.subprocess, "run", fake_run)
    with kbc.connect_closing() as conn:
        _card_with_comments(conn, "feat: size", "Size: S\n## To-dos\n- acme/repo#7", [])
        ko.refresh_closed(conn)
        row = ko.list_outcomes(conn)[0]
    assert row["actual_size"] == "M"
    assert row["size_missed"] == [{"todo": "acme/repo#7", "planned_size": "S", "actual_size": "M",
                                   "files": 2, "lines": 170}]
    assert all(env["GH_CONFIG_DIR"].endswith("/profiles/lina/gh") for _, env in calls)


def test_learning_goal_rebuild_includes_late_links_without_writes(kanban_home):
    with kbc.connect_closing() as conn:
        first, run_id, now = _claimed_card(conn, "feat: first", "Size: S")
        conn.execute("UPDATE task_events SET created_at=? WHERE task_id=? AND kind='claimed'", (now - 3600, first))
        conn.commit()
        goal = kb.create_task(conn, title="Cycle", parents=[first])
        later = kb.create_task(conn, title="feat: later", assignee="lina", goal_mode=True)
        kb.link_tasks(conn, later, goal)
        kb.complete_task(conn, first, result="done", expected_run_id=run_id)
        kb.complete_task(conn, later, result="done")
    rebuilt = json.loads(kc.run_slash("outcomes --rebuild --json"))
    again = json.loads(kc.run_slash("outcomes --rebuild --json"))
    assert len(rebuilt["rows"]) == len(again["rows"]) == 2
    with kbc.connect_closing() as conn:
        before = conn.total_changes
        data = ko.goal_outcome(conn, goal, now=now + 1)
        assert conn.total_changes == before
    assert data["title"] == "Cycle"
    assert {r["task_id"] for r in data["rows"]} == {first, later}
    assert later in {c["task_id"] for c in data["added_later"]}


@pytest.mark.parametrize("files,lines,size", [(2, 150, "S"), (3, 150, "M"), (2, 151, "M"),
                                              (8, 800, "M"), (9, 800, "L"), (8, 801, "L")])
def test_learning_size_thresholds(files, lines, size):
    assert ko.actual_size(files, lines) == size


def test_learning_missing_pr_measurement_stays_unknown(kanban_home, monkeypatch):
    from types import SimpleNamespace

    def fake_run(command, **kwargs):
        if "pr" in command:
            return SimpleNamespace(returncode=1, stdout="")
        return SimpleNamespace(returncode=0, stdout=json.dumps({
            "body": "", "labels": [{"name": "size/M"}],
            "closedByPullRequestsReferences": [{"url": "https://github.com/acme/repo/pull/4"}]}))

    monkeypatch.setattr(ko.subprocess, "run", fake_run)
    result = ko.issue_size({"repo": "acme/repo", "number": 3}, None)
    assert result["planned_size"] == "M"
    assert (result["actual_size"], result["files"], result["lines"]) == (None, None, None)


def test_learning_rebuild_migrates_old_rows_and_cleans_stored_text(kanban_home):
    with kbc.connect_closing() as conn:
        tid, run_id, _now = _claimed_card(conn, "feat: clean", "")
        kb.add_comment(conn, tid, "owner", "token=sk-" + "a" * 50 + " " + "x" * 250 + "\nsecond line")
        kb.complete_task(conn, tid, result="ok", expected_run_id=run_id)
        conn.execute("DROP TABLE work_card_outcomes")
        conn.execute(ko.WORK_CARD_OUTCOMES_DDL)
        conn.commit()
        ko.ensure_table(conn)
        ko.write_outcome(conn, tid)
        row = ko.list_outcomes(conn)[0]
    assert row["task_id"] == tid
    assert row["owner_answers"] == 1
    line = row["touches"][0]["line"]
    assert len(line) <= 200 and "a" * 50 not in line and "\n" not in line


def test_learning_goal_merges_live_cards_and_goal_hands(kanban_home):
    with kbc.connect_closing() as conn:
        first, _run_id, now = _claimed_card(conn, "feat: live", "Size: L")
        goal = kb.create_task(conn, title="active cycle", parents=[first])
        kb.add_comment(conn, first, "lina", "session guidance", source={"surface": "cli"})
        kb.add_comment(conn, goal, "owner", "cycle answer", source={"surface": "app"})
        data = ko.goal_outcome(conn, goal, now=now + 1)
    assert data["rows"][0]["status"] == "running"
    assert [(t["task_id"], t["line"]) for t in data["touches"]] == [(goal, "cycle answer"), (first, "session guidance")]
    assert (data["owner_answers"], data["main_session_touches"]) == (1, 1)
    assert data["sizes"] == [{"task_id": first, "planned_size": "L", "actual_size": None}]
