#!/usr/bin/env python3
"""Audit closed issues: was each one really done?

Behavior tests: tests/ci/test_issue_audit.py. Workflow: .github/workflows/issue-audit.yml.

An issue closed as completed passes when (1) a merged pull request closed it, or it has a
comment starting with ``Evidence:`` (measurement or research issues with no code), and (2) every
item of its ``## Done when`` section is checked or marked ``N/A: <reason>``. A failing issue gets
the ``audit`` label and one comment, rewritten on each run; it is never reopened. When it later
passes, the comment says so and the label comes off.

    python3 -m scripts.ci.issue_audit --repo owner/name (--issue 12 | --days 2) [--dry-run]
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from datetime import UTC, datetime, timedelta
from typing import Any

from scripts.ci import codex_review_gate as gate

AUDIT_LABEL = "audit"
MARKER = "<!-- issue-audit -->"

_ISSUE_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      number body stateReason
      labels(first: 50) { nodes { name } }
      timelineItems(last: 10, itemTypes: [CLOSED_EVENT]) {
        nodes { ... on ClosedEvent { closer { __typename ... on PullRequest { number merged } } } }
      }
      comments(last: 100) { nodes { databaseId body author { login } } }
    }
  }
}
"""


def audit_problems(issue: dict[str, Any]) -> list[str]:
    """``issue`` is the GraphQL shape of ``_ISSUE_QUERY``. Not-planned closes are not audited."""
    if issue.get("stateReason") != "COMPLETED":
        return []
    problems = []
    closers = [(node or {}).get("closer") or {} for node in issue["timelineItems"]["nodes"]]
    merged_pr = bool(closers) and closers[-1].get("__typename") == "PullRequest" \
        and closers[-1].get("merged") is True
    evidence = any((c.get("body") or "").lstrip().startswith("Evidence:")
                   for c in issue["comments"]["nodes"])
    if not merged_pr and not evidence:
        problems.append("Not closed by a merged pull request, and no comment starts with `Evidence:`.")
    items = gate.done_when_items(issue.get("body"))
    if items is None:
        problems.append("The issue has no `## Done when` section.")
    else:
        open_items = [i.text for i in items if not i.checked and not i.na_reason]
        if open_items:
            listed = "\n".join(f"  - {text}" for text in open_items)
            problems.append(f"{len(open_items)} `Done when` item(s) not checked:\n{listed}")
    return problems


def audit_comment(problems: list[str]) -> str:
    if not problems:
        return f"{MARKER}\n**Issue audit:** resolved; this issue now meets its close conditions."
    lines = "\n".join(f"- {p}" for p in problems)
    return (f"{MARKER}\n**Issue audit:** this issue was closed, but:\n\n{lines}\n\n"
            "Fix it by linking the merged pull request, adding an `Evidence:` comment, or checking "
            "the `Done when` items that are done (`N/A: <reason>` for the rest). "
            "The audit reruns daily and updates this comment.")


def _gh(*args: str, payload: dict[str, Any] | None = None) -> str:
    return subprocess.run(["gh", *args], check=True, capture_output=True, text=True,
                          input=json.dumps(payload) if payload is not None else None).stdout


def _issue(repo: str, number: int) -> dict[str, Any]:
    owner, name = repo.split("/", 1)
    data = json.loads(_gh("api", "graphql", "-f", f"query={_ISSUE_QUERY}", "-f", f"owner={owner}",
                          "-f", f"name={name}", "-F", f"number={number}"))
    return data["data"]["repository"]["issue"]


def _recently_closed(repo: str, days: int) -> list[int]:
    since = (datetime.now(UTC) - timedelta(days=days)).strftime("%Y-%m-%dT%H:%M:%SZ")
    issues = gate.gh_list(f"repos/{repo}/issues?state=closed&since={since}&per_page=100")
    return sorted(i["number"] for i in issues
                  if "pull_request" not in i and (i.get("closed_at") or "") >= since)


def _ensure_label(repo: str) -> None:
    try:
        _gh("api", f"repos/{repo}/labels/{AUDIT_LABEL}")
    except subprocess.CalledProcessError:
        _gh("api", "-X", "POST", f"repos/{repo}/labels", "--input", "-", payload={
            "name": AUDIT_LABEL, "color": "d93f0b",
            "description": "Closed issue that does not meet its close conditions"})


def _apply(repo: str, issue: dict[str, Any], problems: list[str]) -> str:
    number = issue["number"]
    mine = [c for c in issue["comments"]["nodes"] if MARKER in (c.get("body") or "")]
    labelled = AUDIT_LABEL in {n["name"] for n in issue["labels"]["nodes"]}
    if not problems and not mine:
        return "ok"
    body = {"body": audit_comment(problems)}
    if mine:
        _gh("api", "-X", "PATCH", f"repos/{repo}/issues/comments/{mine[-1]['databaseId']}",
            "--input", "-", payload=body)
    else:
        _gh("api", "-X", "POST", f"repos/{repo}/issues/{number}/comments", "--input", "-", payload=body)
    if problems and not labelled:
        _ensure_label(repo)
        _gh("api", "-X", "POST", f"repos/{repo}/issues/{number}/labels", "--input", "-",
            payload={"labels": [AUDIT_LABEL]})
    if not problems and labelled:
        _gh("api", "-X", "DELETE", f"repos/{repo}/issues/{number}/labels/{AUDIT_LABEL}")
    return "flagged" if problems else "resolved"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Audit recently closed issues.")
    parser.add_argument("--repo", required=True)
    which = parser.add_mutually_exclusive_group(required=True)
    which.add_argument("--issue", type=int)
    which.add_argument("--days", type=int, help="Audit issues closed in the last N days.")
    parser.add_argument("--dry-run", action="store_true", help="Print the findings, change nothing.")
    args = parser.parse_args(argv)

    numbers = [args.issue] if args.issue else _recently_closed(args.repo, args.days)
    for number in numbers:
        issue = _issue(args.repo, number)
        problems = audit_problems(issue)
        outcome = ("flagged" if problems else "ok") if args.dry_run else _apply(args.repo, issue, problems)
        print(f"#{number}: {outcome}" + "".join(f"\n  {p}" for p in problems))
    return 0


if __name__ == "__main__":
    sys.exit(main())
