#!/usr/bin/env python3
"""Open one follow-up issue per Codex P2/P3 finding of a pull request merged into dev.

Behavior tests: tests/ci/test_review_followups.py. Workflow: .github/workflows/review-followups.yml.

Only findings made on the merged head commit count, at most MAX_ISSUES per pull request. An
issue carries the review comment id in a hidden marker; a finding whose id is already in some
issue body is skipped, so a rerun opens nothing twice. The milestone and the ``Sprint:`` line
come from the issue the pull request closes.

    python3 -m scripts.ci.review_followups --repo owner/name --pr 123 --head-sha <sha> [--dry-run]
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass
from typing import Any

from scripts.ci import codex_review_gate as gate

MAX_ISSUES = 8
MINOR = frozenset({2, 3})
FOLLOW_UP_LABEL = "follow-up"

_MARKER = "<!-- codex-review-comment-id: {} -->"
_MARKER_ID = re.compile(r"<!--\s*codex-review-comment-id:\s*(\d+)\s*-->")
_SCOPE = re.compile(r"^\w+\(([^)]+)\)!?:")
_SPRINT = re.compile(r"^\s*Sprint:.*$", re.MULTILINE)
_NOISE = re.compile(r"<!--.*?-->|^Useful\? React with.*$", re.DOTALL | re.MULTILINE)


@dataclass(frozen=True)
class FollowUp:
    comment_id: int
    title: str
    body: str
    labels: tuple[str, ...]
    milestone: int | None


def area(pr_title: str, path: str) -> str:
    """The pull request title's scope, else the finding's top-level directory or file stem."""
    match = _SCOPE.match(pr_title or "")
    if match:
        return match.group(1)
    if "/" in path:
        return path.split("/", 1)[0]
    return path.rsplit(".", 1)[0] or "repo"


def _sentence_case(title: str) -> str:
    if len(title) > 1 and title[0].isupper() and title[1].islower():
        return title[0].lower() + title[1:]
    return title


def finding_text(body: str) -> str:
    """The finding's explanation without its badge title, reaction prompt and markers."""
    lines = _NOISE.sub("", body).strip().splitlines()
    if lines and gate.finding_priority(lines[0]) is not None:
        lines = lines[1:]
    return "\n".join(lines).strip()


def sprint_line(issue_body: str | None) -> str | None:
    match = _SPRINT.search(issue_body or "")
    return match.group(0).strip() if match else None


def known_comment_ids(issues: list[dict[str, Any]]) -> set[int]:
    return {int(m) for issue in issues for m in _MARKER_ID.findall(issue.get("body") or "")}


def issue_body(finding: gate.Finding, pr: int, sprint: str | None) -> str:
    where = f"`{finding.path}:{finding.line}`" if finding.line else f"`{finding.path}`"
    link = f" ([review comment]({finding.url}))" if finding.url else ""
    return "\n".join([
        "## Summary",
        "",
        f"Codex left a P{finding.priority} finding on {where} in #{pr}{link}:",
        "",
        *(f"> {line}" if line else ">" for line in finding_text(finding.body).splitlines()),
        "",
        "## Done when",
        "",
        f"- [ ] The finding is addressed: {finding.title}.",
        "- [ ] A test fails before the fix and passes after it.",
        "",
        "## Context",
        "",
        f"Follow-up to #{pr}",
        sprint or "Sprint: (none on the original issue)",
        "Size: S",
        "",
        _MARKER.format(finding.comment_id),
    ])


def plan(
    pr: int,
    pr_title: str,
    head_sha: str,
    review_comments: list[dict[str, Any]],
    existing_issues: list[dict[str, Any]],
    origin_issue: dict[str, Any] | None,
    repo_labels: set[str],
) -> list[FollowUp]:
    findings = [f for f in gate.head_findings(head_sha, review_comments) if f.priority in MINOR]
    known = known_comment_ids(existing_issues)
    origin = origin_issue or {}
    milestone = (origin.get("milestone") or {}).get("number")
    sprint = sprint_line(origin.get("body"))
    planned = []
    for finding in findings[:MAX_ISSUES]:
        if finding.comment_id in known:
            continue
        labels = tuple(name for name in (FOLLOW_UP_LABEL, f"P{finding.priority}") if name in repo_labels)
        planned.append(FollowUp(
            comment_id=finding.comment_id,
            title=f"fix({area(pr_title, finding.path)}): {_sentence_case(finding.title)}",
            body=issue_body(finding, pr, sprint),
            labels=labels,
            milestone=milestone,
        ))
    return planned


def _origin_issue(repo: str, pr: int) -> dict[str, Any] | None:
    for ref in gate.closing_references(repo, pr):
        if ((ref.get("repository") or {}).get("nameWithOwner") or "").lower() == repo.lower():
            return gate.gh_json(f"repos/{repo}/issues/{ref['number']}")
    return None


def _create(repo: str, follow_up: FollowUp) -> str:
    payload: dict[str, Any] = {"title": follow_up.title, "body": follow_up.body,
                               "labels": list(follow_up.labels)}
    if follow_up.milestone is not None:
        payload["milestone"] = follow_up.milestone
    out = subprocess.run(
        ["gh", "api", "-X", "POST", f"repos/{repo}/issues", "--input", "-", "--jq", ".html_url"],
        input=json.dumps(payload), check=True, capture_output=True, text=True,
    )
    return out.stdout.strip()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Open follow-up issues for Codex P2/P3 findings.")
    parser.add_argument("--repo", required=True)
    parser.add_argument("--pr", type=int, required=True)
    parser.add_argument("--head-sha", required=True, help="The merged pull request's head commit.")
    parser.add_argument("--dry-run", action="store_true", help="Print the issues, open nothing.")
    args = parser.parse_args(argv)

    pull = gate.gh_json(f"repos/{args.repo}/pulls/{args.pr}")
    repo_labels = {label["name"] for label in gate.gh_list(f"repos/{args.repo}/labels?per_page=100")}
    missing = {FOLLOW_UP_LABEL, "P2", "P3"} - repo_labels
    if missing:
        print(f"::warning::labels missing in {args.repo}, issues open without them: {sorted(missing)}")
    planned = plan(
        args.pr, pull["title"], args.head_sha,
        gate.gh_list(f"repos/{args.repo}/pulls/{args.pr}/comments?per_page=100"),
        gate.gh_list(f"repos/{args.repo}/issues?state=all&per_page=100"),
        _origin_issue(args.repo, args.pr),
        repo_labels,
    )
    print(f"#{args.pr} @ {args.head_sha[:7]}: {len(planned)} follow-up issue(s) to open")
    for follow_up in planned:
        if args.dry_run:
            print(f"--- {follow_up.title} labels={list(follow_up.labels)} milestone={follow_up.milestone}")
            print(follow_up.body)
        else:
            print(f"opened {_create(args.repo, follow_up)}: {follow_up.title}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
