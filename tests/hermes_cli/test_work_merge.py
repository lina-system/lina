"""``lina work merge`` (lina#63): the one "may this head merge" rule as a table, then the merge path against a fake
GitHub: nothing is merged, queued or edited unless every gate passes; a merge commit only; with the merge queue on,
the head is enqueued and the queue's verdict (merged / dropped) is reported; after the merge the linked issue is
closed with its Done when ticked and Codex P2/P3 findings on the merged head go to a follow-up issue."""
from __future__ import annotations

import pytest

from hermes_cli import merge_rules as rules
from hermes_cli.delivery import merge
from tests.hermes_cli._work_fakes import FakeGh

REPO, HEAD, OLD = "acme/app", "h" * 40, "o" * 40
CODEX = {"login": "chatgpt-codex-connector[bot]"}


def run(name, conclusion="success", status="completed", started="2026-11-02T10:00:00Z", run_id=1):
    return {"id": run_id, "name": name, "status": status, "conclusion": conclusion, "started_at": started,
            "completed_at": started, "head_sha": HEAD}


def finding(level, head, path="hermes_cli/x.py", line=3, title="Guard the empty case"):
    return {"id": 70 + line, "user": CODEX, "path": path, "line": line, "commit_id": head, "original_commit_id": head,
            "body": f"**<sub><sub>![{level} Badge](https://img.shields.io/badge/{level}-x)</sub></sub>  {title}**\n\nmore"}


def snapshot(**over) -> dict:
    base = {"repo": REPO, "number": 7, "state": "OPEN", "head": HEAD, "head_ref": "feat/12-thing", "base": "dev",
            "delivery_branch": "dev",
            "body": "## Summary\n\nx\n\nCloses #12\n", "labels": [], "required": ["Lint", "tests"],
            "runs": [run("Lint"), run("tests", run_id=2)], "statuses": [],
            "reviews": [{"user": CODEX, "commit_id": HEAD, "state": "COMMENTED"}], "review_comments": [],
            "issue_comments": [], "dev_ok": True, "dev_failed": [], "issues": {12: {"state": "OPEN"}}}
    base.update(over)
    return base


DEV_RED = {"dev_ok": False, "dev_failed": ["tests", "macOS tests"]}
OWNER = "repo-owner"


def labeled(login, at="2026-11-02T09:00:00Z", event="labeled", name="dev-red-approved"):
    return {"event": event, "actor": {"login": login}, "label": {"name": name}, "created_at": at}


APPROVED = {**DEV_RED, "labels": ["dev-red-approved"], "owner_login": OWNER,
            "runs": [run("Lint"), run("tests", "failure", run_id=2)], "required": ["Lint"]}
CASES = [
    ("required green", {}, rules.PASS, "dev green, no red check on the head"),
    ("one required failed", {"runs": [run("Lint"), run("tests", "failure", run_id=2)]}, rules.FAIL,
     "required check tests: failure"),
    ("required timed out", {"runs": [run("Lint"), run("tests", "timed_out", run_id=2)]}, rules.FAIL,
     "required check tests: timed_out"),
    ("required still running", {"runs": [run("Lint"), run("tests", None, "in_progress", run_id=2)]}, rules.WAIT,
     "required check tests: in_progress"),
    ("required not reported", {"runs": [run("Lint")]}, rules.WAIT, "required check tests: not reported"),
    ("cancelled then green", {"runs": [run("Lint"), run("tests", "cancelled", started="2026-11-02T09:00:00Z"),
                                       run("tests", run_id=3)]}, rules.PASS, "required check tests: success"),
    ("green then a later cancelled run", {"runs": [run("Lint"), run("tests", run_id=2),
                                                   run("tests", "cancelled", started="2026-11-02T11:00:00Z",
                                                       run_id=3)]}, rules.PASS, "required check tests: success"),
    ("only a cancelled run", {"runs": [run("Lint"), run("tests", "cancelled", run_id=2)]}, rules.FAIL,
     "required check tests: cancelled"),
    ("skipped required is green", {"runs": [run("Lint"), run("tests", "skipped", run_id=2)]}, rules.PASS,
     "required check tests: skipped"),
    ("Codex P1 on an older head only", {"review_comments": [finding("P1", OLD)]}, rules.PASS,
     "no P0/P1 findings"),
    ("Codex P1 on the current head", {"review_comments": [finding("P1", HEAD)]}, rules.FAIL,
     "1 P0/P1 finding(s) on hhhhhhh"),
    ("Codex P2 on the current head", {"review_comments": [finding("P2", HEAD)]}, rules.PASS,
     "no P0/P1 findings; 1 P2/P3 finding(s)"),
    ("no Codex review on this head", {"reviews": [{"user": CODEX, "commit_id": OLD}]}, rules.WAIT,
     "Waiting for the Codex review of hhhhhhh"),
    ("Codex summary names this head", {"reviews": [], "issue_comments": [{"user": CODEX, "body":
     '<!-- codex-review-state:v1 {"code_review":{"commit_sha":"' + HEAD + '","status":"completed"}} -->'}]},
     rules.PASS, "Codex reviewed hhhhhhh"),
    ("Codex 'no major issues' comment names this head", {"reviews": [], "issue_comments": [{"user": CODEX, "body":
     f"Codex Review: Didn't find any major issues. Reviewed commit: `{HEAD[:10]}`"}]},
     rules.PASS, "Codex reviewed hhhhhhh"),
    ("Codex 'no major issues' comment names an older head", {"reviews": [], "issue_comments": [{"user": CODEX,
     "body": f"Codex Review: Didn't find any major issues. Reviewed commit: `{OLD[:10]}`"}]},
     rules.WAIT, "Waiting for the Codex review"),
    ("dev red, normal pull request", DEV_RED, rules.FAIL, "dev is red (macOS tests, tests)"),
    ("dev red, dev-red-fix fails fewer and adds none",
     {**DEV_RED, "labels": ["dev-red-fix"], "runs": [run("Lint"), run("tests", run_id=2),
                                                     run("macOS tests", "failure", run_id=3)]},
     rules.PASS, "dev-red-fix clears tests"),
    ("dev red, dev-red-fix adds one",
     {**DEV_RED, "labels": ["dev-red-fix"], "runs": [run("Lint"), run("tests", run_id=2),
                                                     run("macOS tests", "failure", run_id=3),
                                                     run("docs", "failure", run_id=4)]},
     rules.FAIL, "dev-red-fix adds red checks: docs"),
    ("dev red, dev-red-fix only skips dev's red",
     {**DEV_RED, "labels": ["dev-red-fix"], "runs": [run("Lint"), run("tests", "skipped", run_id=2)]},
     rules.FAIL, "shows none of dev's red checks passing"),
    ("dev red, dev-red-approved added by the owner", {**APPROVED, "label_events": [labeled(OWNER)]}, rules.PASS,
     "merging over it: dev-red-approved added by the owner"),
    ("dev red, dev-red-approved added by someone else", {**APPROVED, "label_events": [labeled("someone-else")]},
     rules.FAIL, "added by someone-else, not the owner"),
    ("dev red, dev-red-approved absent", {**APPROVED, "labels": [], "label_events": [labeled(OWNER)]}, rules.FAIL,
     "dev is red (macOS tests, tests)"),
    ("dev red, the owner's label re-added by someone else",
     {**APPROVED, "label_events": [labeled(OWNER), labeled(OWNER, "2026-11-02T09:10:00Z", "unlabeled"),
                                   labeled("someone-else", "2026-11-02T09:20:00Z")]},
     rules.FAIL, "added by someone-else, not the owner"),
    ("dev red, dev-red-approved without work.owner_login",
     {**APPROVED, "owner_login": None, "label_events": [labeled(OWNER)]}, rules.FAIL, "work.owner_login is not set"),
    ("dev red, approved but the head adds a red check",
     {**APPROVED, "label_events": [labeled(OWNER)], "runs": [run("Lint"), run("docs", "failure", run_id=3)]},
     rules.FAIL, "the head adds red checks: docs"),
    ("dev green, a check outside the required list failed", {"runs": [run("Lint"), run("tests", run_id=2),
                                                                      run("docs", "failure", run_id=3)]},
     rules.FAIL, "red checks on the head: docs"),
    ("dev green, a commit status outside the required list failed",
     {"statuses": [{"id": 2, "context": "external-test", "state": "failure"}]}, rules.FAIL,
     "red checks on the head: external-test"),
    ("a commit status failed, then passed",
     {"statuses": [{"id": 2, "context": "external-test", "state": "failure"},
                   {"id": 3, "context": "external-test", "state": "success"}]}, rules.PASS,
     "dev green, no red check on the head"),
    ("the gate's own failed status is judged by its own rule",
     {"statuses": [{"id": 2, "context": "dev green", "state": "failure"}]}, rules.PASS,
     "dev green, no red check on the head"),
    ("no Closes link", {"body": "## Summary\n"}, rules.FAIL, "closes no issue"),
    ("no-issue label", {"body": "", "labels": ["no-issue"], "head_ref": "ci/fix"}, rules.PASS, "no-issue label"),
    ("linked issue already closed", {"issues": {12: {"state": "CLOSED"}}}, rules.FAIL, "no open issue"),
    ("branch names another issue", {"head_ref": "feat/13-thing"}, rules.FAIL, "branch names #13"),
    ("closed pull request", {"state": "CLOSED"}, rules.FAIL, "#7 is closed"),
    ("pull request into main", {"base": "main"}, rules.FAIL, "base is main, not dev"),
]


@pytest.mark.parametrize("over, expected, needle", [c[1:] for c in CASES], ids=[c[0] for c in CASES])
def test_may_merge_table(over, expected, needle):
    verdict = rules.may_merge(snapshot(**over))
    assert verdict.code == expected, verdict.text()
    assert any(needle in line for line in verdict.text()), verdict.text()


def test_tick_done_when_ticks_only_that_section():
    body = "## Summary\n- [ ] not a done-when box\n## Done when\n- [ ] one\n- [x] two\n* [ ] three\n## Context\n- [ ] c\n"
    ticked = rules.tick_done_when(body)
    assert rules.done_when(ticked) == (3, 3)
    assert "- [ ] not a done-when box" in ticked and "- [ ] c" in ticked


def _github(snap: dict, *, queue: bool, queue_answers=(), issue_body="## Done when\n\n- [ ] one\n- [ ] two\n"):
    state = {"merged": False, "polls": list(queue_answers)}
    labels = [{"name": name} for name in snap["labels"]]

    def pull(_query):
        return {"number": 7, "state": "closed" if state["merged"] else "open", "merged": state["merged"],
                "node_id": "PR_node", "head": {"sha": HEAD, "ref": snap["head_ref"]}, "base": {"ref": snap["base"]},
                "body": snap["body"], "labels": labels}

    rule_list = [{"type": "required_status_checks", "parameters": {"required_status_checks": [
        {"context": c} for c in snap["required"]]}}] + ([{"type": "merge_queue"}] if queue else [])
    dev_runs = [{"id": 5, "name": "CI", "status": "completed", "created_at": "2026-11-02T08:00:00Z",
                 "conclusion": "success" if snap["dev_ok"] else "failure"}]
    routes = {
        f"repos/{REPO}/pulls/7": pull, f"repos/{REPO}/rules/branches/dev": rule_list,
        f"repos/{REPO}/actions/runs": {"workflow_runs": dev_runs},
        f"repos/{REPO}/actions/runs/5/jobs": {"jobs": [{"name": n, "conclusion": "failure"} for n in snap["dev_failed"]]},
        f"repos/{REPO}/commits/{HEAD}/check-runs": {"total_count": len(snap["runs"]), "check_runs": snap["runs"]},
        f"repos/{REPO}/commits/{HEAD}/statuses": [], f"repos/{REPO}/pulls/7/reviews": snap["reviews"],
        f"repos/{REPO}/pulls/7/comments": snap["review_comments"], f"repos/{REPO}/issues/7/comments": [],
        f"repos/{REPO}/issues/12": {"number": 12, "state": "open", "body": issue_body},
        f"repos/{REPO}/issues": [], f"repos/{REPO}/issues/7/timeline": snap.get("label_events") or [],
    }

    def graphql(query, variables):
        if "enqueuePullRequest" in query:
            return {"data": {"enqueuePullRequest": {"mergeQueueEntry": {"state": "QUEUED", "position": 1}}}}
        if "isInMergeQueue" in query:
            answer = state["polls"].pop(0)
            state["merged"] = answer["state"] == "MERGED"
            return {"data": {"repository": {"pullRequest": answer}}}
        if "reviewThreads" in query:
            return {"data": {"repository": {"pullRequest": {"reviewThreads": {"nodes": []}}}}}
        raise AssertionError(f"unexpected query {query[:40]}")

    def on_run(args):
        if args[:2] == ["pr", "merge"]:
            state["merged"] = True
        return ""

    return FakeGh(routes, graphql, on_run)


def _merge(gh, lines):
    return merge.merge_pr(gh, REPO, 7, out=lines.append, sleep=lambda _s: None, poll_s=60, queue_timeout_s=600)


@pytest.mark.parametrize("over", [{"runs": [run("Lint"), run("tests", "failure", run_id=2)]},
                                  {"review_comments": [finding("P1", HEAD)]}, DEV_RED], ids=["red", "p1", "dev-red"])
def test_a_refused_head_is_neither_merged_queued_nor_edited(over):
    gh, lines = _github(snapshot(**over), queue=True), []
    assert _merge(gh, lines) == rules.FAIL
    assert gh.writes == [] and gh.commands == []
    assert not any("enqueuePullRequest" in q for q, _ in gh.queries)
    assert lines[-1] == "#7 not merged: fail"


def test_without_a_queue_it_merges_with_a_merge_commit_then_closes_the_issue_and_files_followups():
    snap = snapshot(review_comments=[finding("P2", HEAD), finding("P3", OLD, line=9), finding("P1", OLD, line=11)])
    gh, lines = _github(snap, queue=False), []
    assert _merge(gh, lines) == rules.PASS
    assert gh.commands == [["pr", "merge", "7", "-R", REPO, "--merge", "--match-head-commit", HEAD]]
    assert not any(flag in gh.commands[0] for flag in ("--admin", "--squash", "--rebase"))
    patch = next(w for w in gh.writes if w[0] == "PATCH")
    assert patch[1] == f"repos/{REPO}/issues/12" and patch[2]["state"] == "closed"
    assert rules.done_when(patch[2]["body"]) == (2, 2)
    created = [w for w in gh.writes if w[0] == "POST"]
    assert len(created) == 1 and created[0][2]["title"] == "chore(hermes_cli): review follow-ups"
    assert created[0][2]["labels"] == ["follow-up"]
    assert created[0][2]["body"].count("- [ ] P") == 1 and "(from #7)" in created[0][2]["body"]
    assert "#12 closed, Done when 2/2 ticked" in lines


def test_with_the_queue_on_the_head_is_enqueued_and_reported_merged():
    answers = [{"isInMergeQueue": True, "state": "OPEN"}, {"isInMergeQueue": False, "state": "MERGED"}]
    gh, lines = _github(snapshot(), queue=True, queue_answers=answers), []
    assert _merge(gh, lines) == rules.PASS
    enqueue = [v for q, v in gh.queries if "enqueuePullRequest" in q]
    assert enqueue == [{"id": "PR_node", "head": HEAD}]
    assert gh.commands == [], "with a queue the merge is the queue's, never gh pr merge"
    assert "#7 merged by the merge queue" in lines
    assert any(w[0] == "PATCH" for w in gh.writes)


def test_with_the_queue_on_a_dropped_entry_is_reported_and_nothing_is_closed():
    gh, lines = _github(snapshot(), queue=True, queue_answers=[{"isInMergeQueue": False, "state": "OPEN"}]), []
    assert _merge(gh, lines) == rules.FAIL
    assert lines[-1].startswith("#7 dropped from the merge queue")
    assert gh.writes == []


def test_followups_skip_a_finding_already_listed(monkeypatch):
    from hermes_cli.delivery import followups
    listed = followups.box(followups.findings([finding("P2", HEAD)], HEAD, set())[0], 3)
    issue = {"number": 40, "title": "chore(hermes_cli): review follow-ups", "labels": [{"name": "follow-up"}],
             "body": f"## Done when\n\n{listed}\n\n## Context\n\nSprint: S1\nSize: S\n"}
    snap = snapshot(review_comments=[finding("P2", HEAD), finding("P3", HEAD, line=8, title="Name the file")])
    gh = _github(snap, queue=False)
    gh.routes[f"repos/{REPO}/issues"] = [issue]
    gh.routes[f"repos/{REPO}/issues/12"] = {"number": 12, "state": "open", "body": "## Done when\n\n- [ ] x\n"}

    from hermes_cli.delivery import settings
    monkeypatch.setattr(settings, "sprint", lambda base=None: (None, 1))
    lines = followups.file(gh, REPO, 7, HEAD)
    assert lines == ["chore(hermes_cli): review follow-ups: added 1 to #40"]
    body = gh.writes[0][2]["body"]
    assert body.count("Guard the empty case") == 1 and "Name the file" in body and "Follow-up to #7" in body



def test_an_owner_approved_head_merges_over_a_red_dev_and_the_timeline_is_read(monkeypatch):
    from hermes_cli.delivery import settings
    monkeypatch.setattr(settings, "owner_login", lambda base=None: OWNER)
    snap = snapshot(**{**APPROVED, "label_events": [labeled(OWNER)]})
    gh, lines = _github(snap, queue=False), []
    assert _merge(gh, lines) == rules.PASS, lines
    assert any(path.startswith(f"repos/{REPO}/issues/7/timeline") for path in gh.reads)
    assert gh.commands[0][:2] == ["pr", "merge"]


def test_a_label_by_someone_else_is_refused_through_the_merge_path(monkeypatch):
    from hermes_cli.delivery import settings
    monkeypatch.setattr(settings, "owner_login", lambda base=None: OWNER)
    gh, lines = _github(snapshot(**{**APPROVED, "label_events": [labeled("someone-else")]}), queue=False), []
    assert _merge(gh, lines) == rules.FAIL
    assert gh.commands == [] and gh.writes == []


def test_the_merge_verdict_on_codex_is_the_required_status_verdict():
    """``lina work merge`` and the ``Codex review`` status share one recognition (``codex_review_gate``)."""
    gate = rules.review_gate()
    cases = [snapshot(review_comments=[finding("P1", HEAD)]), snapshot(reviews=[]),
             snapshot(reviews=[], issue_comments=[{"user": CODEX, "body": f"No major issues found in {HEAD[:7]}."}])]
    for snap in cases:
        status = gate.review_status(HEAD, snap["reviews"], snap["review_comments"], snap["issue_comments"])
        verdict = rules.codex_review(HEAD, snap["reviews"], snap["review_comments"], snap["issue_comments"])
        assert verdict.lines == [({"success": rules.PASS, "failure": rules.FAIL, "pending": rules.WAIT}[status.state],
                                  status.description)]


def test_a_pull_request_into_another_branch_is_refused_before_any_merge_or_issue_edit():
    gh, lines = _github(snapshot(base="main"), queue=False), []
    gh.routes[f"repos/{REPO}/rules/branches/main"] = []
    assert _merge(gh, lines) == rules.FAIL
    assert "FAIL base is main, not dev" in lines
    assert gh.commands == [] and gh.writes == [] and gh.queries == []
    runs = [path for path in gh.reads if path.startswith(f"repos/{REPO}/actions/runs?")]
    assert runs and all("branch=dev&" in path for path in runs), "dev green is read on the delivery branch"
    assert not any(path.startswith(f"repos/{REPO}/rules/branches/main") for path in gh.reads)
