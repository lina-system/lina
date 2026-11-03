"""``lina work merge`` (lina#63): the one "may this head merge" rule as a table, then the merge path against a fake
GitHub: nothing is merged, queued or edited unless every gate passes; a merge commit only; with the merge queue on,
the head is enqueued and the queue's verdict (merged / dropped) is reported; after the merge the linked issue is
closed with its Done when ticked and Codex P2/P3 findings on the merged head go to a follow-up issue."""
from __future__ import annotations

from pathlib import Path

import pytest

from hermes_cli import merge_rules as rules
from hermes_cli.delivery import merge
from tests.hermes_cli._work_fakes import FakeGh

REPO_ROOT = Path(__file__).resolve().parents[2]
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
    ("green then a queued rerun with no times yet", {"runs": [run("Lint"), run("tests", run_id=2),
                                                         {"id": 3, "name": "tests", "status": "queued",
                                                          "conclusion": None, "started_at": None,
                                                          "completed_at": None, "head_sha": HEAD}]},
     rules.WAIT, "required check tests: queued"),
    ("a newer rerun green after an older failure", {"runs": [run("Lint"), run("tests", "failure", run_id=2),
                                                            run("tests", started="2026-11-02T09:00:00Z",
                                                                run_id=3)]},
     rules.PASS, "required check tests: success"),
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


@pytest.mark.parametrize("creator,state,expected", [
    ("a-user", "success", rules.WAIT),
    ("other-ci[bot]", "success", rules.WAIT),
    ("custom-ci[bot]", "success", rules.PASS),
    ("custom-ci[bot]", "failure", rules.FAIL),
    ("custom-ci[bot]", "pending", rules.WAIT),
])
def test_collected_app_pin_controls_status_creator(creator, state, expected):
    """The real collector must not erase the pin before the merge rule sees the head (#153)."""
    gh = _github(snapshot(required=["tests"], runs=[
        {**run("discovery"), "app": {"id": 8675309, "slug": "custom-ci"}}]), queue=False)
    gh.routes[f"repos/{REPO}/rules/branches/dev"] = [{"type": "required_status_checks", "parameters": {
        "required_status_checks": [{"context": "tests", "integration_id": 8675309}]}}]
    gh.routes[f"repos/{REPO}/commits/{HEAD}/statuses"] = [
        {"id": 8, "context": "tests", "state": state, "creator": {"login": creator}}]

    collected = merge.collect_pr(gh, REPO, 7)
    verdict = rules.may_merge(collected)

    assert verdict.code == expected, verdict.text()
    if creator != "custom-ci[bot]":
        assert "WAIT required check tests: not reported on the head yet" in verdict.text()
    assert collected["required"] == [("tests", 8675309)]
    assert gh.commands == [] and gh.writes == []


def test_collector_keeps_distinct_app_pins_for_the_same_context():
    from hermes_cli.delivery.github import required_contexts

    required = required_contexts([{"type": "required_status_checks", "parameters": {
        "required_status_checks": [{"context": "tests", "integration_id": app_id}
                                   for app_id in (None, 42, 42)]}}])
    statuses = [{"id": 8, "context": "tests", "state": "success", "creator": {"login": "a-user"}}]

    verdict = rules.required_checks(required, [], statuses)

    assert verdict.code == rules.WAIT, verdict.text()
    assert [code for code, _ in verdict.lines] == [rules.PASS, rules.WAIT]
    assert required == [("tests", None), ("tests", 42)]


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



def test_two_merges_filing_into_one_follow_up_issue_keep_both_findings(tmp_path, monkeypatch):
    """Two ``lina work merge`` runs finishing in the same area and sprint: each reads the follow-up issue, then
    writes its whole body. Without one lock around read and write the second write dropped the first one's
    findings (or two issues were opened). The fake issue store answers reads from what was written."""
    import threading

    from hermes_cli.delivery import followups, settings
    from hermes_cli.worker import settings as worker_settings
    monkeypatch.setattr(settings, "sprint", lambda base=None: (None, 1))
    monkeypatch.setattr(worker_settings, "root", lambda base=None: tmp_path / "lina")
    store = {"issues": [{"number": 40, "title": "chore(hermes_cli): review follow-ups",
                         "labels": [{"name": "follow-up"}], "body": "## Done when\n\n## Context\n\nSprint: S1\n"}]}
    read_both = threading.Barrier(2, timeout=2)

    def gh_for(pr, title):
        snap = snapshot(number=pr, review_comments=[finding("P2", HEAD, line=pr, title=title)])
        gh = _github(snap, queue=False)
        gh.routes[f"repos/{REPO}/pulls/{pr}/comments"] = snap["review_comments"]

        def issues(_query):
            rows = [dict(i) for i in store["issues"]]
            try:
                read_both.wait()  # both runs read before either writes: the race, made certain
            except threading.BrokenBarrierError:
                pass  # serialized: the other run already finished, no partner arrives
            return rows
        gh.routes[f"repos/{REPO}/issues"] = issues

        def write(method, path, payload=None):
            gh.writes.append((method, path, payload))
            if method == "PATCH":
                store["issues"][0]["body"] = payload["body"]
            return {"number": 41}
        gh.write = write
        return gh

    results = {}
    runs = [threading.Thread(target=lambda pr=pr, title=title: results.setdefault(
        pr, followups.file(gh_for(pr, title), REPO, pr, HEAD))) for pr, title in ((7, "Guard A"), (8, "Guard B"))]
    for thread in runs:
        thread.start()
    for thread in runs:
        thread.join(10)
    body = store["issues"][0]["body"]
    assert "Guard A" in body and "Guard B" in body, body
    assert all(line == ["chore(hermes_cli): review follow-ups: added 1 to #40"] for line in results.values()), results


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


def test_the_codex_review_rules_come_from_the_package_not_the_source_tree(monkeypatch):
    """An installed ``lina`` has no ``scripts/``: the verdict must come from ``hermes_cli.codex_review_gate``, and a
    reviewed head with no P0/P1 passes there (before the fix it waited forever without the source tree)."""
    from hermes_cli import codex_review_gate
    assert rules.review_gate() is codex_review_gate
    assert "scripts" not in Path(codex_review_gate.__file__).resolve().relative_to(REPO_ROOT).parts
    reviewed = snapshot()
    verdict = rules.codex_review(HEAD, reviewed["reviews"], reviewed["review_comments"], reviewed["issue_comments"])
    assert verdict.lines and verdict.lines[0][0] == rules.PASS, verdict.lines


def test_dev_state_finds_the_ci_run_past_the_first_page_of_other_workflows():
    """The deciding dev run is the newest finished run of the configured workflow, wherever it sits: a first page
    full of other workflows' runs (Honcho, LCM-X, CodeQL) once read as \"no run, so green\" over a red CI."""
    from hermes_cli.delivery.github import dev_state
    from tests.hermes_cli._work_fakes import FakeGh
    others = [{"id": 100 + i, "name": "Honcho", "status": "completed", "conclusion": "success",
               "created_at": f"2026-11-03T{10 + i // 60:02d}:{i % 60:02d}:00Z"} for i in range(130)]
    red_ci = {"id": 5, "name": "CI", "status": "completed", "conclusion": "failure", "created_at": "2026-11-03T09:00:00Z"}

    def runs(query):
        page = int(dict(p.split("=", 1) for p in query.split("&")).get("page", "1"))
        rows = others + [red_ci]
        return {"workflow_runs": rows[(page - 1) * 100:page * 100], "total_count": len(rows)}

    gh = FakeGh({f"repos/{REPO}/actions/runs": runs,
                 f"repos/{REPO}/actions/runs/5/jobs": {"jobs": [{"name": "tests", "conclusion": "failure"}]}})
    assert dev_state(gh, REPO, "dev", "CI") == (False, {"tests"})
    nothing = FakeGh({f"repos/{REPO}/actions/runs": lambda q: {"workflow_runs": [], "total_count": 0}})
    assert dev_state(nothing, REPO, "dev", "CI") == (True, set())


def test_dev_state_counts_a_red_run_being_rerun_as_red_not_an_older_green():
    """Re-running a failed dev run puts it back to ``in_progress`` (``run_attempt`` 2): a read of completed runs
    only skipped it and let the older green run decide. A first attempt still going is not waited on."""
    from hermes_cli.delivery.github import dev_state
    from tests.hermes_cli._work_fakes import FakeGh
    green = {"id": 4, "name": "CI", "status": "completed", "conclusion": "success", "run_attempt": 1,
             "created_at": "2026-11-03T08:00:00Z"}
    rerun = {"id": 5, "name": "CI", "status": "in_progress", "conclusion": None, "run_attempt": 2,
             "created_at": "2026-11-03T09:00:00Z"}
    fresh = {"id": 6, "name": "CI", "status": "in_progress", "conclusion": None, "run_attempt": 1,
             "created_at": "2026-11-03T10:00:00Z"}

    def gh_with(rows):
        return FakeGh({f"repos/{REPO}/actions/runs": lambda q: {"workflow_runs": [
            r for r in rows if "status=completed" not in q or r["status"] == "completed"]}})
    ok, red = dev_state(gh_with([rerun, green]), REPO, "dev", "CI")
    assert not ok and red, red
    assert dev_state(gh_with([fresh, green]), REPO, "dev", "CI") == (True, set())


def test_merge_reads_every_page_of_the_head_statuses():
    """A gate run posts three statuses each time it runs, so a busy head has more than 100: a context past the
    first page (a required external one) must still be seen."""
    from hermes_cli.delivery import merge
    statuses = [{"id": 1000 - i, "context": "Codex review", "state": "success"} for i in range(120)] + [
        {"id": 1, "context": "external CI", "state": "success"}]
    gh, _lines = _github(snapshot(required=["Lint", "external CI"]), queue=False), []
    gh.pages = lambda path: statuses if "/statuses" in path else list(gh.json(path) or [])
    snap = merge.collect_pr(gh, REPO, 7)
    assert any(s["context"] == "external CI" for s in snap["statuses"])
    assert rules.required_checks(["external CI"], [], snap["statuses"]).code == rules.PASS


def test_followups_read_every_page_of_review_threads():
    """A finding resolved in a thread past the first 100 is resolved: it is not filed again."""
    from hermes_cli.delivery import followups
    from tests.hermes_cli._work_fakes import FakeGh
    pages = {None: {"pageInfo": {"hasNextPage": True, "endCursor": "c1"},
                    "nodes": [{"isResolved": False, "comments": {"nodes": [{"databaseId": i}]}} for i in range(100)]},
             "c1": {"pageInfo": {"hasNextPage": False, "endCursor": None},
                    "nodes": [{"isResolved": True, "comments": {"nodes": [{"databaseId": 501}]}}]}}
    gh = FakeGh(graphql=lambda q, v: {"data": {"repository": {"pullRequest": {"reviewThreads": pages[v.get("after")]}}}})
    assert 501 in followups._resolved(gh, REPO, 7)


def test_a_pull_request_into_another_branch_is_refused_before_any_merge_or_issue_edit():
    gh, lines = _github(snapshot(base="main"), queue=False), []
    gh.routes[f"repos/{REPO}/rules/branches/main"] = []
    assert _merge(gh, lines) == rules.FAIL
    assert "FAIL base is main, not dev" in lines
    assert gh.commands == [] and gh.writes == [] and gh.queries == []
    runs = [path for path in gh.reads if path.startswith(f"repos/{REPO}/actions/runs?")]
    assert runs and all("branch=dev&" in path for path in runs), "dev green is read on the delivery branch"
    assert not any(path.startswith(f"repos/{REPO}/rules/branches/main") for path in gh.reads)


def _stateful(gh, fail_first_issue_patch=False):
    """Writes land in the fake: an issue PATCH updates the issue, a POST adds the follow-up issue to the list."""
    issue = gh.routes[f"repos/{REPO}/issues/12"]
    listed = gh.routes[f"repos/{REPO}/issues"]
    failing = {"left": 1 if fail_first_issue_patch else 0}

    def write(method, path, payload=None):
        gh.writes.append((method, path, payload))
        if method == "PATCH" and path == f"repos/{REPO}/issues/12":
            if failing["left"]:
                failing["left"] -= 1
                from hermes_cli.delivery.github import GhError
                raise GhError("gh api -X exited 1: HTTP 502")
            issue.update(payload)
        elif method == "POST":
            made = {"number": 900, "title": payload["title"], "body": payload["body"],
                    "labels": [{"name": n} for n in payload["labels"]]}
            listed.append(made)
            return made
        return {"number": 900}

    gh.write = write
    return issue


def test_a_failed_post_merge_request_is_finished_by_a_re_run():
    from hermes_cli.delivery.github import GhError
    gh, lines = _github(snapshot(review_comments=[finding("P2", HEAD)]), queue=False), []
    issue = _stateful(gh, fail_first_issue_patch=True)
    with pytest.raises(GhError):
        _merge(gh, lines)
    assert gh.commands == [["pr", "merge", "7", "-R", REPO, "--merge", "--match-head-commit", HEAD]]
    assert issue["state"] == "open", "the merge went through, the issue update did not"

    lines.clear()
    assert _merge(gh, lines) == rules.PASS, lines
    assert "#7 is already merged: finishing the post-merge steps" in lines
    assert len(gh.commands) == 1, "no second merge"
    assert issue["state"] == "closed" and rules.done_when(issue["body"]) == (2, 2)
    assert [w for w in gh.writes if w[0] == "POST"] and "#12 closed, Done when 2/2 ticked" in lines


def test_a_second_re_run_after_a_finished_merge_changes_nothing():
    gh, lines = _github(snapshot(review_comments=[finding("P2", HEAD)]), queue=False), []
    _stateful(gh)
    assert _merge(gh, lines) == rules.PASS
    done = list(gh.writes)
    lines.clear()
    assert _merge(gh, lines) == rules.PASS
    assert gh.writes == done, "no re-tick, no re-close, no duplicate follow-up"
    assert "#12 already closed, Done when 2/2 ticked" in lines
    assert "chore(hermes_cli): review follow-ups: every finding already listed" in lines
    assert len(gh.commands) == 1
