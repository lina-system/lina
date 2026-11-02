"""``kanban gate prs|dev|artifact|review|ask`` (lina-system/lina#109).

Scratch boards are built through ``kanban_db`` in a temp HERMES_HOME; GitHub
answers come from a fake ``gh`` on PATH that replays recorded JSON. Each gate
passes on good input and fails (exit 1) or waits (exit 75) on the inputs it
must refuse.
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_gates as gates
from hermes_cli.kanban_db_connect import connect

GATES_FILE = Path(gates.__file__)
MERGE, HEAD, PR_HEAD = "m" * 40, "h" * 40, "a" * 40

FAKE_GH = """#!{python}
import json, os, sys
argv = " ".join(sys.argv[1:])
with open(os.environ["FAKE_GH_LOG"], "a") as log:
    log.write(argv.replace("\\n", " ") + "\\n")
for needles, rc, out in json.load(open(os.environ["FAKE_GH_RECORDS"])):
    if all(n in argv for n in needles):
        sys.stdout.write(json.dumps(out))
        sys.exit(rc)
sys.stderr.write("fake gh: no record for " + argv)
sys.exit(1)
"""


@pytest.fixture
def board(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.delenv("HERMES_KANBAN_TASK", raising=False)
    kb.init_db()
    db = kb.kanban_db_path()
    monkeypatch.setenv("HERMES_KANBAN_DB", str(db))
    return db


@pytest.fixture
def gh(tmp_path, monkeypatch):
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    script = bin_dir / "gh"
    script.write_text(FAKE_GH.format(python=sys.executable))
    script.chmod(0o755)
    records, log = tmp_path / "gh.json", tmp_path / "gh.log"
    log.write_text("")
    monkeypatch.setenv("PATH", f"{bin_dir}{os.pathsep}{os.environ['PATH']}")
    monkeypatch.setenv("FAKE_GH_RECORDS", str(records))
    monkeypatch.setenv("FAKE_GH_LOG", str(log))

    class Gh:
        def set(self, recs):
            records.write_text(json.dumps(recs))

        def calls(self):
            return log.read_text().splitlines()

    return Gh()


def _card(body="", assignee="alice"):
    with connect() as conn:
        return kb.create_task(conn, title="work card", body=body, assignee=assignee)


def _run(*argv):
    return gates.main(list(argv))


TODOS = "Goal slice.\n\n## To-dos\n\n- acme/app#12\n- [ ] write the note\n\n## Notes\n- acme/app#99\n"


def _issue(state="CLOSED", body="## Done when\n\n- [x] one\n- [x] two\n", merged=True):
    pr = {"number": 34, "url": "https://github.com/acme/app/pull/34", "merged": merged,
          "mergedAt": "2026-11-01T10:00:00Z", "mergeCommit": {"oid": MERGE},
          "repository": {"nameWithOwner": "acme/app"}}
    issue = {"state": state, "stateReason": "COMPLETED" if state == "CLOSED" else None, "body": body,
             "closedByPullRequestsReferences": {"nodes": [pr]}}
    return [["closedByPullRequestsReferences", "number=12"], 0, {"data": {"repository": {"issue": issue}}}]


def _acceptance(conclusion="success", status="completed"):
    required = {"data": {"repository": {"pullRequest": {
        "headRefOid": PR_HEAD, "baseRefName": "dev", "state": "MERGED",
        "baseRef": {"branchProtectionRule": {"requiredStatusChecks": [
            {"context": "tests", "app": {"databaseId": 1}}]}}}}}}
    run = {"id": 7, "name": "tests", "head_sha": PR_HEAD, "app": {"id": 1}, "status": status,
           "conclusion": conclusion, "html_url": "https://github.com/acme/app/actions/runs/7"}
    return [
        [["pullRequest(number:34)"], 0, required],
        [["rules/branches/dev"], 0, [[]]],
        [["commits/" + PR_HEAD + "/check-runs"], 0, [{"total_count": 1, "check_runs": [run]}]],
        [["commits/" + PR_HEAD + "/statuses"], 0, [[]]],
        [["repos/acme/app/pulls/34"], 0, {"head": {"sha": PR_HEAD}, "base": {"ref": "dev"},
                                         "state": "closed", "merged": True}],
    ]



def test_prs_passes_when_issue_closed_by_green_merged_pr(board, gh, capsys):
    tid = _card(TODOS)
    gh.set([_issue(), *_acceptance()])
    assert _run("prs", "--card", tid) == gates.PASS
    out = capsys.readouterr().out
    assert "PASS acme/app#12" in out and "Done when 2/2" in out
    assert not any("number=99" in c for c in gh.calls()), "only ## To-dos lines are to-dos"


@pytest.mark.parametrize("record, accept, expected, why", [
    (_issue(body="## Done when\n\n- [x] one\n- [ ] two\n"), _acceptance(), gates.FAIL, "Done when 1/2"),
    (_issue(state="OPEN"), _acceptance(), gates.FAIL, "still open"),
    (_issue(merged=False), _acceptance(), gates.FAIL, "not closed by a merged"),
    (_issue(body="no section"), _acceptance(), gates.FAIL, "no Done when"),
    (_issue(), _acceptance("failure"), gates.FAIL, "checks failure (tests)"),
    (_issue(), _acceptance(None, "in_progress"), gates.WAIT, "checks pending"),
])
def test_prs_refuses(board, gh, capsys, record, accept, expected, why):
    tid = _card(TODOS)
    gh.set([record, *accept])
    assert _run("prs", "--card", tid) == expected
    assert why in capsys.readouterr().out


@pytest.mark.parametrize("runs, expected", [
    ([("success", "2026-11-01T09:00:00Z"), ("cancelled", "2026-11-01T09:30:00Z")], gates.PASS),
    ([("cancelled", "2026-11-01T09:00:00Z"), ("success", "2026-11-01T09:30:00Z")], gates.PASS),
    ([("skipped", "2026-11-01T09:00:00Z")], gates.PASS),
    ([("cancelled", "2026-11-01T09:00:00Z")], gates.FAIL),
], ids=["green-then-cancelled", "cancelled-then-green", "skipped", "only-cancelled"])
def test_prs_reads_the_merge_rule_for_each_required_check(board, gh, runs, expected):
    """``gate prs`` and ``lina work merge`` judge a check the same way (lina#63, ``merge_rules``): by its latest
    non-cancelled run, with skipped green."""
    tid = _card(TODOS)
    records = _acceptance()
    records[2] = [["commits/" + PR_HEAD + "/check-runs"], 0, [{"total_count": len(runs), "check_runs": [
        {"id": 7 + i, "name": "tests", "head_sha": PR_HEAD, "app": {"id": 1}, "status": "completed",
         "conclusion": conclusion, "started_at": at, "html_url": f"https://github.com/acme/app/actions/runs/{7 + i}"}
        for i, (conclusion, at) in enumerate(runs)]}]]
    gh.set([_issue(), *records])
    assert _run("prs", "--card", tid) == expected
    assert any("filter=all" in call for call in gh.calls() if "check-runs" in call)


@pytest.mark.parametrize("labels, actor, owner, expected, why", [
    (["dev-red-approved"], "repo-owner", "repo-owner", gates.PASS, "checks green"),
    (["dev-red-approved"], "someone-else", "repo-owner", gates.FAIL, "added by someone-else, not the owner"),
    (["dev-red-approved"], "repo-owner", None, gates.FAIL, "work.owner_login is not set"),
    ([], None, "repo-owner", gates.PASS, "checks green"),
], ids=["owner", "someone-else", "no-owner-configured", "absent"])
def test_prs_accepts_dev_red_approved_only_from_the_owner(board, gh, capsys, labels, actor, owner, expected, why):
    """A merged pull request labelled ``dev-red-approved`` merged over a red dev: ``gate prs`` asks the merge
    rule's ``owner_approved`` whether the owner added the label (lina#63)."""
    tid = _card(TODOS)
    issue = _issue()
    issue[2]["data"]["repository"]["issue"]["closedByPullRequestsReferences"]["nodes"][0]["labels"] = {
        "nodes": [{"name": name} for name in labels]}
    timeline = [{"event": "labeled", "actor": {"login": actor}, "label": {"name": "dev-red-approved"},
                 "created_at": "2026-11-01T09:00:00Z"}]
    gh.set([issue, [["issues/34/timeline"], 0, timeline], *_acceptance()])
    argv = ["prs", "--card", tid] + (["--owner-login", owner] if owner else [])
    assert _run(*argv) == expected
    assert why in capsys.readouterr().out
    assert any("issues/34/timeline" in c for c in gh.calls()) is bool(labels)


def test_prs_fails_without_issue_todos_and_defaults_card_to_env(board, gh, monkeypatch, capsys):
    tid = _card("## To-dos\n\n- [ ] only a checklist\n")
    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    gh.set([])
    assert _run("prs") == gates.FAIL
    assert f"{tid}: no owner/repo#n issues" in capsys.readouterr().out



def _dev(status="completed", conclusion="success", macos="success", compare="ahead"):
    runs = [{"id": 501, "name": "CI", "head_sha": HEAD, "status": status, "conclusion": conclusion,
             "created_at": "2026-11-01T10:05:00Z"},
            {"id": 502, "name": "Honcho", "head_sha": HEAD, "status": "completed", "conclusion": "success",
             "created_at": "2026-11-01T10:05:01Z"},
            {"id": 400, "name": "CI", "head_sha": "o" * 40, "status": "completed", "conclusion": "failure",
             "created_at": "2026-10-30T10:00:00Z"}]
    ci_jobs = [{"name": "tests", "status": "completed", "conclusion": "success"},
               {"name": "macos", "status": "completed" if macos else "in_progress", "conclusion": macos}]
    return [
        _issue(),
        [["actions/runs?branch=dev&event=push"], 0, {"workflow_runs": runs}],
        [[f"compare/{MERGE}...{HEAD}"], 0, {"status": compare}],
        [["actions/runs/501/jobs"], 0, {"jobs": ci_jobs}],
        [["actions/runs/502/jobs"], 0, {"jobs": [{"name": "unit", "status": "completed", "conclusion": "success"}]}],
    ]


def test_dev_passes_when_every_push_run_at_head_after_merge_is_green(board, gh, capsys):
    tid = _card(TODOS)
    gh.set(_dev())
    assert _run("dev", "acme/app", "--card", tid) == gates.PASS
    out = capsys.readouterr().out
    assert "PASS CI run 501" in out and "PASS Honcho run 502" in out
    assert "run 400" not in out, "older runs on another head do not decide the gate"


@pytest.mark.parametrize("kw, expected, why", [
    ({"conclusion": "failure", "macos": "failure"}, gates.FAIL, "failed jobs: macos"),
    ({"status": "in_progress", "conclusion": None, "macos": None}, gates.WAIT, "still running (macos)"),
    ({"compare": "behind"}, gates.WAIT, "is before merge"),
])
def test_dev_refuses(board, gh, capsys, kw, expected, why):
    tid = _card(TODOS)
    gh.set(_dev(**kw))
    assert _run("dev", "acme/app", "--card", tid) == expected
    assert why in capsys.readouterr().out


def test_dev_fails_when_card_has_no_merge_in_repo(board, gh, capsys):
    tid = _card(TODOS)
    gh.set(_dev())
    assert _run("dev", "acme/other", "--card", tid) == gates.FAIL
    assert "no merged pull request" in capsys.readouterr().out



def test_artifact_passes_and_shows_check_output(tmp_path, capfd):
    target = tmp_path / "report.md"
    target.write_text("# done\n")
    assert _run("artifact", str(target), "--check", f"grep -c done {target}") == gates.PASS
    out = capfd.readouterr().out
    assert "\n1\n" in out and "check exited 0" in out


def test_artifact_folder_needs_one_non_empty_file(tmp_path):
    folder = tmp_path / "out"
    (folder / "sub").mkdir(parents=True)
    (folder / "sub" / "empty.txt").write_text("")
    assert _run("artifact", str(folder), "--check", "true") == gates.FAIL
    (folder / "sub" / "real.txt").write_text("x")
    assert _run("artifact", str(folder), "--check", "true") == gates.PASS


@pytest.mark.parametrize("make, check", [
    (lambda p: None, "true"),
    (lambda p: p.write_text(""), "true"),
    (lambda p: p.write_text("data"), "echo nope; exit 3"),
])
def test_artifact_refuses(tmp_path, make, check):
    target = tmp_path / "deliverable.txt"
    make(target)
    assert _run("artifact", str(target), "--check", check) == gates.FAIL



def test_artifact_check_that_hangs_fails(tmp_path):
    target = tmp_path / "deliverable.txt"
    target.write_text("data")
    assert _run("artifact", str(target), "--check", "exec sleep 30", "--timeout", "0.5") == gates.FAIL


def _review_flow(tid, reviewer):
    with connect() as conn:
        assert kb.request_review(conn, tid, summary="ready", reviewer=reviewer, force=True)
        claimed = kb.claim_review_task(conn, tid)
        assert claimed is not None
        assert kb.complete_task(conn, tid, result="looks good", expected_run_id=claimed.current_run_id)


def test_review_passes_on_completed_review_run_by_another_profile(board, capsys):
    tid = _card()
    _review_flow(tid, "bob")
    assert _run("review", "--card", tid) == gates.PASS
    assert "by bob completed" in capsys.readouterr().out


def test_review_by_the_assignee_itself_does_not_count(board, capsys):
    tid = _card()
    _review_flow(tid, "alice")
    with connect() as conn:
        kb.add_comment(conn, tid, "alice", "approve: all done")
    assert _run("review", "--card", tid) == gates.FAIL
    assert "no approval by anyone other than alice" in capsys.readouterr().out


def test_review_pending_waits(board):
    tid = _card()
    with connect() as conn:
        assert kb.request_review(conn, tid, summary="ready", reviewer="bob", force=True)
    assert _run("review", "--card", tid) == gates.WAIT


@pytest.mark.parametrize("author, body, expected", [
    ("user", "승인합니다", gates.PASS),
    ("user", "Approved, ship it", gates.PASS),
    ("user", "not yet, approve later", gates.FAIL),
    ("carol", "approve", gates.FAIL),
])
def test_review_owner_comment(board, author, body, expected):
    tid = _card()
    with connect() as conn:
        kb.add_comment(conn, tid, author, body)
    assert _run("review", "--card", tid) == expected


def test_review_owner_name_is_configurable(board):
    tid = _card()
    with connect() as conn:
        kb.add_comment(conn, tid, "jun", "승인")
    assert _run("review", "--card", tid) == gates.FAIL
    assert _run("review", "--card", tid, "--owner", "jun") == gates.PASS



def test_ask_waits_until_someone_else_answers(board, capsys):
    tid = _card()
    with connect() as conn:
        kb.add_comment(conn, tid, "alice", "질문: which database?")
        kb.add_comment(conn, tid, "alice", "still waiting on the question above")
    assert _run("ask", "--card", tid) == gates.WAIT
    with connect() as conn:
        kb.add_comment(conn, tid, "user", "Use SQLite.")
        kb.add_comment(conn, tid, "alice", "Q: and the schema?")
    assert _run("ask", "--card", tid) == gates.WAIT
    with connect() as conn:
        kb.add_comment(conn, tid, "bob", "Reuse the existing one.")
    capsys.readouterr()
    assert _run("ask", "--card", tid) == gates.PASS
    out = capsys.readouterr().out
    assert "answered by user" in out and "answered by bob" in out


def test_ask_passes_without_questions_and_ignores_others_questions(board):
    tid = _card()
    with connect() as conn:
        kb.add_comment(conn, tid, "user", "Q: is this a question for alice?")
    assert _run("ask", "--card", tid) == gates.PASS



def test_board_is_opened_read_only(board):
    tid = _card()
    conn = gates.open_board(str(board))
    with pytest.raises(Exception, match="readonly"):
        conn.execute("UPDATE tasks SET title = 'x' WHERE id = ?", (tid,))
    conn.close()


def test_runs_as_a_plain_file_against_the_board(board, tmp_path):
    tid = _card()
    with connect() as conn:
        kb.add_comment(conn, tid, "alice", "질문: ready?")
    env = {**os.environ, "HERMES_KANBAN_TASK": tid, "PYTHONPATH": ""}
    result = subprocess.run([sys.executable, "-I", str(GATES_FILE), "ask"], cwd=tmp_path, env=env, check=False,
                            capture_output=True, text=True, encoding="utf-8", timeout=60)
    assert result.returncode == gates.WAIT, result.stdout + result.stderr
    assert "unanswered: 질문: ready?" in result.stdout


def test_hermes_kanban_gate_cli_entry(board, capsys):
    from hermes_cli import kanban as kanban_cli
    from hermes_cli import kanban_parser

    tid = _card()
    root = argparse.ArgumentParser()
    kanban_parser.build_parser(root.add_subparsers(dest="command"))
    args = root.parse_args(["kanban", "gate", "review", "--card", tid])
    assert kanban_cli.kanban_command(args) == gates.FAIL
    assert "gate review: fail" in capsys.readouterr().out
