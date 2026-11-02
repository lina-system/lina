"""Behavior tests for the Codex P2/P3 follow-up issues (scripts/ci/review_followups.py)."""

from typing import Any

from scripts.ci import review_followups as followups

BOT = {"login": "chatgpt-codex-connector[bot]"}
HEAD = "c6f2e9eb117fc4601abd170f8f716d55f61d0b94"
OLD = "1111111111111111111111111111111111111111"
LABELS = {"follow-up", "P2", "P3"}
ORIGIN = {"number": 45, "milestone": {"number": 3, "title": "M0"},
          "body": "## Summary\n\nBring LINA Helper in.\n\nSprint: 2026-W44\nSize: M"}


def _finding(cid: int, priority: int, title: str, commit: str = HEAD, **extra: Any) -> dict[str, Any]:
    body = (f"**<sub><sub>![P{priority} Badge](https://img.shields.io/badge/P{priority}-yellow"
            f"?style=flat)</sub></sub>  {title}**\n\nOn an installation with only the bundled "
            "driver, recovery fails.\n\nUseful? React with 👍 / 👎.\n\n<!-- codex-code-review-finding:v1 -->")
    return {"id": cid, "user": BOT, "original_commit_id": commit, "commit_id": HEAD,
            "path": "plugins/computer_use/lina_helper/__init__.py", "line": 99, "body": body,
            "html_url": f"https://github.com/lina-system/lina/pull/57#discussion_r{cid}", **extra}


def _plan(comments: list[dict[str, Any]], existing: list[dict[str, Any]] | None = None,
          origin: dict[str, Any] | None = ORIGIN, labels: set[str] = LABELS) -> list[followups.FollowUp]:
    return followups.plan(57, "feat(devices): bring LINA Helper into LINA", HEAD, comments,
                          existing or [], origin, labels)


def test_one_issue_per_p2_p3_finding_on_the_merged_head():
    comments = [
        _finding(4438138367, 2, "Preserve endpoint-based driver resolution during recovery"),
        _finding(4438138368, 1, "Scope selected Mac targets by profile"),
        _finding(4438138369, 3, "Name the helper socket", commit=OLD),
        _finding(4438138370, 3, "Reply, not a finding", in_reply_to_id=4438138367),
    ]
    [issue] = _plan(comments)
    assert issue.comment_id == 4438138367
    assert issue.title == "fix(devices): preserve endpoint-based driver resolution during recovery"
    assert issue.labels == ("follow-up", "P2")
    assert issue.milestone == 3


def test_body_sections_and_context():
    [issue] = _plan([_finding(4438138367, 2, "Preserve endpoint-based driver resolution")])
    body = issue.body
    for heading in ("## Summary", "## Done when", "## Context"):
        assert heading in body
    context = body.split("## Context", 1)[1].splitlines()
    assert "Follow-up to #57" in context
    assert "Sprint: 2026-W44" in context
    assert "Size: S" in context
    assert "<!-- codex-review-comment-id: 4438138367 -->" in context
    assert "> On an installation with only the bundled driver, recovery fails." in body
    assert "Useful? React" not in body


def test_existing_issue_with_the_comment_id_is_not_opened_again():
    comments = [_finding(1, 2, "First"), _finding(2, 3, "Second")]
    [first, second] = _plan(comments)
    existing = [{"number": 70, "body": first.body}]
    assert [f.comment_id for f in _plan(comments, existing)] == [second.comment_id]


def test_at_most_eight_issues_per_pull_request():
    comments = [_finding(cid, 2, f"Finding {cid}") for cid in range(1, 12)]
    assert [f.comment_id for f in _plan(comments)] == list(range(1, followups.MAX_ISSUES + 1))


def test_missing_origin_issue_and_labels():
    [issue] = _plan([_finding(1, 3, "Something")], origin=None, labels=set())
    assert issue.milestone is None
    assert issue.labels == ()
    assert "Sprint: (none on the original issue)" in issue.body


def test_area_falls_back_to_the_finding_path():
    assert followups.area("Merge branch dev", "tools/lina_helper/actions.py") == "tools"
    assert followups.area("chore: tidy", "cli.py") == "cli"
    assert followups.area("fix(ci)!: break", "x/y.py") == "ci"
