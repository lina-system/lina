"""Behavior tests for the merge gates in scripts/ci/codex_review_gate.py.

Regression for #55 and #57, merged with Codex P1 findings on their head commits.
"""

import importlib.util
import sys
from pathlib import Path
from typing import Any

import pytest

SCRIPT = Path(__file__).resolve().parents[2] / "scripts" / "ci" / "codex_review_gate.py"
_spec = importlib.util.spec_from_file_location("codex_review_gate", SCRIPT)
assert _spec and _spec.loader
gate = importlib.util.module_from_spec(_spec)
sys.modules[_spec.name] = gate
_spec.loader.exec_module(gate)

BOT = {"login": "chatgpt-codex-connector[bot]"}
OLD = "bbdcc5d8324516b0b551d46dbb9d754b12c32030"
HEAD = "0e96e7e854b120cec9926e79377afa2ad002e0ff"
REPO = "lina-system/lina"


def _badge(priority: int, title: str) -> str:
    return (f"**<sub><sub>![P{priority} Badge](https://img.shields.io/badge/P{priority}-orange"
            f"?style=flat)</sub></sub>  {title}**\n\nDetails.\n\nUseful? React with 👍 / 👎.\n\n"
            "<!-- codex-code-review-finding:v1 -->")


def _review(commit: str) -> dict[str, Any]:
    return {"id": 1, "user": BOT, "commit_id": commit, "state": "COMMENTED",
            "body": "<!-- codex-review-receipt:v1 -->\n### 💡 Codex Review"}


def _finding(cid: int, commit: str, priority: int, title: str) -> dict[str, Any]:
    return {"id": cid, "user": BOT, "commit_id": HEAD, "original_commit_id": commit,
            "path": "plugins/memory/honcho/client.py", "line": 10, "body": _badge(priority, title)}


def _summary(commit: str, status: str = "completed", findings: bool = False) -> dict[str, Any]:
    flag = str(findings).lower()
    state = (f'{{"code_review":{{"commit_sha":"{commit}","has_any_findings":{flag},'
             f'"has_unresolved_findings":{flag},"status":"{status}"}}}}')
    return {"id": 9, "user": BOT, "body": f"<!-- codex-pull-request-review-summary -->\n"
            f"<!-- codex-review-state:v1 {state} -->\n## Codex Review Summary"}


CASES = {
    "no review": ([], [], [], "pending"),
    "review only on an earlier head": (
        [_review(OLD)], [_finding(5, OLD, 1, "Flush buffered messages")], [_summary(OLD)], "pending"),
    "clean review summary on the new head": (
        [_review(OLD)], [_finding(5, OLD, 1, "Flush buffered messages")], [_summary(HEAD)], "success"),
    "P1 on the head": (
        [_review(HEAD)], [_finding(7, HEAD, 2, "Minor"), _finding(6, HEAD, 1, "Gate attribution")],
        [_summary(HEAD, findings=True)], "failure"),
    "P2 only on the head": (
        [_review(HEAD)], [_finding(6, HEAD, 2, "Preserve driver resolution")],
        [_summary(HEAD, findings=True)], "success"),
    "no major issues comment": (
        [], [], [{"id": 3, "user": BOT, "updated_at": "2026-11-02T02:00:00Z",
                  "body": "Codex Review: Didn't find any major issues. Keep them coming!"}], "success"),
}


@pytest.mark.parametrize("reviews,comments,issue_comments,expected", CASES.values(), ids=CASES.keys())
def test_review_status(reviews, comments, issue_comments, expected):
    status = gate.review_status(HEAD, reviews, comments, issue_comments, "2026-11-02T01:30:00Z")
    assert status.state == expected
    assert len(status.description) <= gate.DESCRIPTION_LIMIT


def test_failure_counts_blocking_findings_and_names_the_first():
    reviews, comments, issue_comments, _ = CASES["P1 on the head"]
    comments = [*comments, _finding(8, HEAD, 0, "Second blocker")]
    status = gate.review_status(HEAD, reviews, comments, issue_comments)
    assert status.description == "2 P0/P1 finding(s) on 0e96e7e; first: Gate attribution"


def test_no_major_issues_comment_older_than_the_head_does_not_count():
    old = [{"id": 3, "user": BOT, "updated_at": "2026-11-01T00:00:00Z",
            "body": "Codex Review: Didn't find any major issues."}]
    assert gate.review_status(HEAD, [], [], old, "2026-11-02T01:30:00Z").state == "pending"


def test_comments_from_other_authors_never_count():
    human = {"login": "thisisjun786"}
    reviews = [{**_review(HEAD), "user": human}]
    comments = [{**_finding(6, HEAD, 1, "Not Codex"), "user": human}]
    issue_comments = [{**_summary(HEAD), "user": human}]
    assert gate.review_status(HEAD, reviews, comments, issue_comments).state == "pending"


def test_failed_codex_review_stays_pending():
    status = gate.review_status(HEAD, [], [], [_summary(HEAD, status="failed", findings=True)])
    assert status.state == "pending" and "@codex review" in status.description


def _issue(number: int, state: str = "OPEN", repo: str = REPO) -> dict[str, Any]:
    return {"number": number, "state": state, "repository": {"nameWithOwner": repo}}


LINKED = {
    "no Closes": ("feat/14-honcho-adapter", [], "failure"),
    "closed issue": ("feat/14-honcho-adapter", [_issue(14, "CLOSED")], "failure"),
    "issue of another repository": ("feat/14-honcho-adapter", [_issue(14, repo="lina-system/lina-app")],
                                    "failure"),
    "branch number not closed": ("feat/14-honcho-adapter", [_issue(15)], "failure"),
    "linked": ("feat/14-honcho-adapter", [_issue(14)], "success"),
    "several issues, branch names one": ("fix/14-x", [_issue(15), _issue(14)], "success"),
    "branch without a number": ("ci/merge-gates", [_issue(61)], "success"),
}


def test_no_issue_label_passes_without_an_issue():
    status = gate.linked_issue_status(REPO, "fix/typo", [], frozenset({"no-issue", "bug"}))
    assert status.state == "success"
    assert gate.linked_issue_status(REPO, "fix/typo", [], frozenset({"bug"})).state == "failure"


@pytest.mark.parametrize("branch,closing,expected", LINKED.values(), ids=LINKED.keys())
def test_linked_issue_status(branch, closing, expected):
    status = gate.linked_issue_status(REPO, branch, closing)
    assert status.state == expected
    assert len(status.description) <= gate.DESCRIPTION_LIMIT


DEV_GREEN = {"conclusion": "success", "head_sha": "df74379616710654957481dda9035122cad73760"}
DEV_RED = {"conclusion": "failure", "head_sha": "df74379616710654957481dda9035122cad73760"}
RED_JOBS = frozenset({"Prune check", "Python tests (macOS)"})


def _check(name: str, conclusion: str | None = "success", status: str = "completed") -> dict[str, Any]:
    return {"name": name, "status": status, "conclusion": conclusion}


FIX = frozenset({"dev-red-fix"})
DEV = {
    "dev green": (DEV_GREEN, frozenset(), frozenset(), [], "success"),
    "dev red, not a fix: wait": (DEV_RED, RED_JOBS, frozenset({"bug"}), [_check("Lint")], "pending"),
    "fix removes a red, adds none": (DEV_RED, RED_JOBS, FIX, [
        _check("Prune check"), _check("Python tests (macOS)", "failure"), _check("Lint"),
        _check("Set merge gate statuses", None, "in_progress")], "success"),
    "fix adds a red": (DEV_RED, RED_JOBS, FIX, [_check("Prune check"), _check("Lint", "failure")], "failure"),
    "fix removes nothing": (DEV_RED, RED_JOBS, FIX, [
        _check("Prune check", "failure"), _check("Python tests (macOS)", "failure")], "failure"),
    "fix still running": (DEV_RED, RED_JOBS, FIX, [_check("Prune check", None, "in_progress")], "pending"),
    "fix only skips dev's red": (DEV_RED, RED_JOBS, FIX, [
        _check("Prune check", "skipped"), _check("Python tests (macOS)", "skipped"), _check("Lint")], "failure"),
    "no completed dev run": (None, frozenset(), frozenset(), [], "pending"),
}


def test_owner_approval_is_the_only_way_over_a_red_dev():
    labels = frozenset({"dev-red-approved"})
    assert gate.dev_green_status(DEV_RED, RED_JOBS, labels, [], owner_approved=True).state == "success"
    assert gate.dev_green_status(DEV_RED, RED_JOBS, labels, [], owner_approved=False).state == "pending"
    assert gate.dev_green_status(DEV_RED, RED_JOBS, frozenset(), [], owner_approved=True).state == "pending"


@pytest.mark.parametrize("run,red,labels,checks,expected", DEV.values(), ids=DEV.keys())
def test_dev_green_status(run, red, labels, checks, expected):
    status = gate.dev_green_status(run, red, labels, checks)
    assert status.state == expected
    assert len(status.description) <= gate.DESCRIPTION_LIMIT
