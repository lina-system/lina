#!/usr/bin/env python3
"""Merge gates for a pull request: the ``Codex review``, ``Linked issue`` and ``Done when`` statuses.

Behavior tests: tests/ci/test_codex_review_gate.py. Workflow: .github/workflows/codex-review-gate.yml.

``Codex review`` (docs/ci.md):

- success: ``chatgpt-codex-connector[bot]`` reviewed the head commit (a pull request review on
  it, its review-summary comment marking a finished review of it with no findings, or a
  "no major issues" comment newer than it), and none of its review comments on that commit
  carries a P0 or P1 badge;
- failure: the bot left P0/P1 findings on the head commit (count and first finding title);
- pending: no review of the head commit yet. A review of an earlier head never counts.

``Linked issue``:

- success: the body closes (``Closes``/``Fixes #n``, GitHub ``closingIssuesReferences``) at
  least one open issue of this repository, and a branch named ``<type>/<n>-<slug>`` closes #n;
- failure otherwise, including a pull request opened without an issue.

``Done when``:

- success: the body's ``## Done when`` items are those of the linked issues (compared after
  ``normalize_item``), and each is checked or carries ``N/A: <reason>``;
- failure: no section, or items missing, unchecked or not in the issue (named in the description).

    python3 scripts/ci/codex_review_gate.py --repo owner/name --pr 123 [--dry-run]
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass
from datetime import datetime
from typing import Any

CODEX_BOT = "chatgpt-codex-connector[bot]"
REVIEW_CONTEXT = "Codex review"
LINKED_CONTEXT = "Linked issue"
DONE_CONTEXT = "Done when"
BLOCKING = frozenset({0, 1})
DESCRIPTION_LIMIT = 140  # GitHub rejects longer commit status descriptions

_BADGE = re.compile(r"!\[P([0-3]) Badge\]")
_TITLE = re.compile(r"!\[P[0-3] Badge\][^\n]*?(?:</sub>)+\s*(.+?)\s*\*\*")
_STATE = re.compile(r"<!--\s*codex-review-state:v1\s*(\{.*?\})\s*-->", re.DOTALL)
_NO_MAJOR = re.compile(r"(?:\bno|n[’']t find any) major issues", re.IGNORECASE)
_BRANCH_ISSUE = re.compile(r"^[a-z]+/(\d+)-")
_HEADING = re.compile(r"^##\s+(.+?)\s*$")
_ITEM = re.compile(r"^[-*]\s+(?:\[([ xX])\]\s+)?(.*)$")
_NA = re.compile(r"\s*(?:[-—–:]\s*)?\bN/A:\s*(.*)$")
_MARKUP = re.compile(r"[`*_]")


@dataclass(frozen=True)
class Status:
    state: str
    description: str


@dataclass(frozen=True)
class Finding:
    comment_id: int
    priority: int
    title: str
    path: str
    line: int | None
    body: str
    url: str


def _limit(text: str) -> str:
    return text if len(text) <= DESCRIPTION_LIMIT else text[: DESCRIPTION_LIMIT - 1] + "…"


def _is_bot(item: dict[str, Any]) -> bool:
    return (item.get("user") or {}).get("login") == CODEX_BOT


def _parse_time(value: str | None) -> datetime | None:
    return datetime.fromisoformat(value) if value else None


def finding_priority(body: str) -> int | None:
    match = _BADGE.search(body or "")
    return int(match.group(1)) if match else None


def finding_title(body: str) -> str:
    match = _TITLE.search(body or "")
    return match.group(1).strip() if match else "(untitled finding)"


def head_findings(head_sha: str, review_comments: list[dict[str, Any]]) -> list[Finding]:
    """Codex findings made on ``head_sha``, oldest first. Replies are not findings."""
    found = []
    for comment in review_comments:
        if not _is_bot(comment) or comment.get("in_reply_to_id"):
            continue
        if comment.get("original_commit_id") != head_sha:
            continue
        priority = finding_priority(comment.get("body") or "")
        if priority is None:
            continue
        found.append(Finding(
            comment_id=int(comment["id"]),
            priority=priority,
            title=finding_title(comment["body"]),
            path=comment.get("path") or "",
            line=comment.get("line") or comment.get("original_line"),
            body=comment["body"],
            url=comment.get("html_url") or "",
        ))
    return sorted(found, key=lambda f: f.comment_id)


def _summary_state(issue_comments: list[dict[str, Any]]) -> dict[str, Any]:
    """``code_review`` of the bot's review-summary comment (it edits one comment per PR)."""
    for comment in issue_comments:
        if not _is_bot(comment):
            continue
        match = _STATE.search(comment.get("body") or "")
        if match:
            try:
                return (json.loads(match.group(1)).get("code_review") or {})
            except json.JSONDecodeError:
                return {}
    return {}


def _no_major_comment(head_sha: str, head_date: datetime | None,
                      issue_comments: list[dict[str, Any]]) -> bool:
    """A "no major issues" comment that names the head commit or is newer than it."""
    for comment in issue_comments:
        body = comment.get("body") or ""
        if not _is_bot(comment) or not _NO_MAJOR.search(body):
            continue
        if head_sha[:7] in body:
            return True
        updated = _parse_time(comment.get("updated_at") or comment.get("created_at"))
        if head_date and updated and updated >= head_date:
            return True
    return False


def review_status(
    head_sha: str,
    reviews: list[dict[str, Any]],
    review_comments: list[dict[str, Any]],
    issue_comments: list[dict[str, Any]],
    head_date: str | None = None,
) -> Status:
    short = head_sha[:7]
    findings = head_findings(head_sha, review_comments)
    blocking = [f for f in findings if f.priority in BLOCKING]
    if blocking:
        return Status("failure", _limit(
            f"{len(blocking)} P0/P1 finding(s) on {short}; first: {blocking[0].title}"))
    state = _summary_state(issue_comments)
    reviewed = (
        any(_is_bot(r) and r.get("commit_id") == head_sha for r in reviews)
        or (state.get("commit_sha") == head_sha and state.get("status") == "completed"
            and state.get("has_any_findings") is False)
        or _no_major_comment(head_sha, _parse_time(head_date), issue_comments)
    )
    if reviewed:
        minor = len(findings)
        tail = f"; {minor} P2/P3 finding(s) become follow-up issues" if minor else ""
        return Status("success", _limit(f"Codex reviewed {short}: no P0/P1 findings{tail}"))
    if state.get("commit_sha") == head_sha and state.get("status") == "failed":
        return Status("pending", f"Codex could not review {short}; comment '@codex review'")
    return Status("pending", f"Waiting for the Codex review of {short}")


def linked_issue_status(repo: str, branch: str, closing: list[dict[str, Any]]) -> Status:
    """``closing`` is GraphQL ``closingIssuesReferences.nodes``: number, state, repository."""
    if not closing:
        return Status("failure", "No 'Closes #n': open an issue first and link it in the body")
    same_repo = [c for c in closing
                 if ((c.get("repository") or {}).get("nameWithOwner") or "").lower() == repo.lower()]
    open_numbers = sorted(int(c["number"]) for c in same_repo if c.get("state") == "OPEN")
    if not open_numbers:
        if not same_repo:
            return Status("failure", f"'Closes' names no issue of {repo}")
        closed = ", ".join(f"#{c['number']}" for c in same_repo)
        return Status("failure", _limit(f"Linked issue {closed} is closed; link an open issue"))
    match = _BRANCH_ISSUE.match(branch or "")
    if match and int(match.group(1)) not in open_numbers:
        listed = ", ".join(f"#{n}" for n in open_numbers)
        return Status("failure", _limit(
            f"Branch {branch} names #{match.group(1)} but the PR closes {listed}"))
    return Status("success", _limit("Closes " + ", ".join(f"#{n}" for n in open_numbers)))


@dataclass(frozen=True)
class DoneItem:
    text: str
    checked: bool
    na_reason: str | None

    @property
    def key(self) -> str:
        return normalize_item(self.text)


def normalize_item(text: str) -> str:
    """Compare items by words: no markup, case, spacing or trailing punctuation."""
    return " ".join(_MARKUP.sub("", text).lower().split()).rstrip(".;:,")


def done_when_items(markdown: str | None) -> list[DoneItem] | None:
    """Top-level bullets of the ``## Done when`` section, or None when there is no section.

    An item is its first line; indented continuation lines are detail, not part of the key.
    """
    items: list[DoneItem] | None = None
    for line in (markdown or "").splitlines():
        heading = _HEADING.match(line)
        if heading:
            if items is not None:
                break
            if heading.group(1).strip().lower() == "done when":
                items = []
            continue
        if items is None:
            continue
        item = _ITEM.match(line)
        if not item:
            continue
        text = item.group(2).strip()
        na = _NA.search(text)
        if na:
            text = text[: na.start()].strip()
        items.append(DoneItem(text, (item.group(1) or " ").lower() == "x",
                              na.group(1).strip() if na else None))
    return items


def done_when_status(pr_body: str | None, issues: list[dict[str, Any]]) -> Status:
    """``issues`` are the open issues the pull request closes, each with ``number`` and ``body``."""
    if not issues:
        return Status("failure", "No linked open issue to compare 'Done when' with")
    pr_items = done_when_items(pr_body)
    if pr_items is None:
        return Status("failure", "PR body has no '## Done when' section")
    wanted: dict[str, str] = {}
    for issue in issues:
        items = done_when_items(issue.get("body"))
        if items is None:
            return Status("failure", f"Issue #{issue['number']} has no '## Done when' section")
        wanted.update((item.key, item.text) for item in items)
    given = {item.key for item in pr_items}
    missing = [text for key, text in wanted.items() if key not in given]
    extra = [item.text for item in pr_items if item.key not in wanted]
    unchecked = [item.text for item in pr_items
                 if item.key in wanted and not item.checked and not item.na_reason]
    problems = [f"{label} {len(texts)}: {texts[0]}" for label, texts in
                (("missing", missing), ("unchecked", unchecked), ("not in the issue", extra)) if texts]
    if problems:
        return Status("failure", _limit("; ".join(problems)))
    na = sum(1 for item in pr_items if item.na_reason)
    return Status("success", f"{len(pr_items)} item(s) done" + (f", {na} N/A" if na else ""))


def _gh(*args: str) -> str:
    return subprocess.run(["gh", *args], check=True, capture_output=True, text=True).stdout


def gh_json(path: str) -> Any:
    return json.loads(_gh("api", path))


def gh_list(path: str) -> list[dict[str, Any]]:
    out = _gh("api", "--paginate", path, "--jq", ".[]")
    return [json.loads(line) for line in out.splitlines() if line.strip()]


_CLOSING_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      closingIssuesReferences(first: 50) {
        nodes { number state body repository { nameWithOwner } }
      }
    }
  }
}
"""


def closing_references(repo: str, pr: int) -> list[dict[str, Any]]:
    owner, name = repo.split("/", 1)
    data = json.loads(_gh("api", "graphql", "-f", f"query={_CLOSING_QUERY}",
                          "-f", f"owner={owner}", "-f", f"name={name}", "-F", f"number={pr}"))
    pull = data["data"]["repository"]["pullRequest"]
    return pull["closingIssuesReferences"]["nodes"]


def post_status(repo: str, sha: str, context: str, status: Status, target_url: str) -> None:
    _gh("api", "-X", "POST", f"repos/{repo}/statuses/{sha}",
        "-f", f"state={status.state}", "-f", f"context={context}",
        "-f", f"description={status.description}", "-f", f"target_url={target_url}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Set the Codex review and Linked issue statuses.")
    parser.add_argument("--repo", required=True)
    parser.add_argument("--pr", type=int, required=True)
    parser.add_argument("--target-url", default="")
    parser.add_argument("--dry-run", action="store_true", help="Print the statuses, post nothing.")
    args = parser.parse_args(argv)

    pull = gh_json(f"repos/{args.repo}/pulls/{args.pr}")
    head_sha = pull["head"]["sha"]
    closing = closing_references(args.repo, args.pr)
    head_date = gh_json(f"repos/{args.repo}/commits/{head_sha}")["commit"]["committer"]["date"]
    statuses = {
        REVIEW_CONTEXT: review_status(
            head_sha,
            gh_list(f"repos/{args.repo}/pulls/{args.pr}/reviews?per_page=100"),
            gh_list(f"repos/{args.repo}/pulls/{args.pr}/comments?per_page=100"),
            gh_list(f"repos/{args.repo}/issues/{args.pr}/comments?per_page=100"),
            head_date,
        ),
        LINKED_CONTEXT: linked_issue_status(args.repo, pull["head"]["ref"], closing),
    }
    open_issues = [ref for ref in closing
                   if ref["repository"]["nameWithOwner"].lower() == args.repo.lower()
                   and ref["state"] == "OPEN"]
    statuses[DONE_CONTEXT] = done_when_status(pull.get("body"), open_issues)
    target = args.target_url or pull["html_url"]
    for context, status in statuses.items():
        print(f"{context} @ {head_sha[:7]}: {status.state} - {status.description}")
        if not args.dry_run:
            post_status(args.repo, head_sha, context, status, target)
    return 0


if __name__ == "__main__":
    sys.exit(main())
