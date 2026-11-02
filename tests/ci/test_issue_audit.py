"""Behavior tests for the closed-issue audit (scripts/ci/issue_audit.py)."""

from typing import Any

from scripts.ci import issue_audit as audit

DONE = "## Summary\n\nx\n\n## Done when\n\n- [x] The connector loads.\n- [ ] Docs say so. N/A: no docs page\n\n## Context\n"


def _issue(closer: dict[str, Any] | None = None, comments: tuple[str, ...] = (), body: str = DONE,
           reason: str = "COMPLETED") -> dict[str, Any]:
    return {
        "number": 14, "body": body, "stateReason": reason, "labels": {"nodes": []},
        "timelineItems": {"nodes": [{"closer": closer}] if closer is not None else []},
        "comments": {"nodes": [{"databaseId": i, "body": b, "author": {"login": "x"}}
                               for i, b in enumerate(comments)]},
    }


MERGED_PR = {"__typename": "PullRequest", "number": 55, "merged": True}


def test_closed_by_a_merged_pull_request_passes():
    assert audit.audit_problems(_issue(MERGED_PR)) == []


def test_evidence_comment_passes_without_a_pull_request():
    assert audit.audit_problems(_issue({}, ("  Evidence: p95 is 180 ms on run 42.",))) == []


def test_neither_pull_request_nor_evidence_is_flagged():
    unmerged = {"__typename": "PullRequest", "number": 9, "merged": False}
    for issue in (_issue(), _issue(unmerged, ("Done, see logs.",))):
        [problem] = audit.audit_problems(issue)
        assert "Evidence:" in problem


def test_unchecked_done_when_item_is_flagged():
    body = DONE.replace("- [x] The connector loads.", "- [ ] The connector loads.")
    [problem] = audit.audit_problems(_issue(MERGED_PR, body=body))
    assert problem.startswith("1 `Done when` item(s) not checked")
    assert "The connector loads." in problem


def test_missing_section_is_flagged_and_not_planned_is_skipped():
    assert audit.audit_problems(_issue(MERGED_PR, body="## Summary\n")) == [
        "The issue has no `## Done when` section."]
    assert audit.audit_problems(_issue(body="## Summary\n", reason="NOT_PLANNED")) == []


def test_comment_carries_the_marker_it_is_found_by():
    assert audit.MARKER in audit.audit_comment(["x"])
    assert audit.MARKER in audit.audit_comment([])
