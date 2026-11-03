"""Merge gates for a pull request: the ``Codex review``, ``Linked issue`` and ``dev green`` statuses.

Behavior tests: tests/ci/test_codex_review_gate.py. Workflow: .github/workflows/codex-review-gate.yml.

``Codex review`` (docs/ci.md):

- success: ``chatgpt-codex-connector[bot]`` reviewed the head commit (a pull request review on
  it, its review-summary comment marking a finished review of it, or a
  "no major issues" comment naming it), and none of its review comments on that commit
  carries a P0 or P1 badge;
- failure: the bot left P0/P1 findings on the head commit (count and first finding title);
- pending: no review of the head commit yet. A review of an earlier head never counts.

``Linked issue``:

- success: the body closes (``Closes``/``Fixes #n``, GitHub ``closingIssuesReferences``) at
  least one open issue of this repository, and a branch named ``<type>/<n>-<slug>`` closes #n;
- success also when the pull request carries the ``no-issue`` label (a typo, a one-line CI fix);
- failure otherwise, including a pull request opened without an issue.

``dev green``: only the pull request that fixes a red ``dev`` may merge while ``dev`` is red.

- success: the latest completed push CI run on ``dev`` passed; or the pull request carries the
  ``dev-red-fix`` label, at least one of that run's red jobs passes on its head (skipped is not
  passing) and the head adds no red;
- pending: ``dev`` is red and the pull request is not a fix (wait for ``dev`` to turn green),
  or a fix whose checks are still running;
- failure: a ``dev-red-fix`` pull request that adds a red check or makes none of dev's red pass;
- success also when a repository admin (the owner) put the ``dev-red-approved`` label on the pull
  request after seeing why ``dev`` must stay red (docs/delivery.md, step 7).

In the merge queue the three statuses must also report on the queue's merge commit, or the
queue waits on them for good. ``--merge-group`` judges the pull request the queue ref names
(``gh-readonly-queue/dev/pr-<n>-<sha>``) exactly as on its head and sets the same verdicts on
the queue commit.

``--all`` lists open pull requests into ``dev`` and judges each one afterwards. A pull request
that is no longer open at judging time (``state != "open"`` or ``merged``) is skipped: one line
is printed and nothing is posted. A merge that lands mid-sweep has just closed its issue, and a
red ``Linked issue #n is closed`` on that head would be stale.

    python3 scripts/ci/codex_review_gate.py --repo owner/name (--pr 123 | --all) [--dry-run]
    python3 scripts/ci/codex_review_gate.py --repo owner/name --merge-group <head_ref> --sha <sha>

This module ships in the package, because ``lina work merge`` (``hermes_cli.merge_rules``) reads the same
verdict in an installed copy that has no ``scripts/``. It imports only the standard library: the workflow
checks out this file and its ``scripts/ci`` entry point alone and runs them with a bare ``python3``.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass
from typing import Any

CODEX_BOT = "chatgpt-codex-connector[bot]"
REVIEW_CONTEXT = "Codex review"
LINKED_CONTEXT = "Linked issue"
BLOCKING = frozenset({0, 1})
NO_ISSUE_LABEL = "no-issue"
DEV_CONTEXT = "dev green"
DEV_FIX_LABEL = "dev-red-fix"
DEV_APPROVED_LABEL = "dev-red-approved"
RED = frozenset({"failure", "cancelled", "timed_out", "action_required", "startup_failure"})
GATE_JOB = "Set merge gate statuses"
DESCRIPTION_LIMIT = 140  # GitHub rejects longer commit status descriptions
GH_TIMEOUT_S = 120  # one gh api call; the workflow job itself is capped at 10 minutes

_BADGE = re.compile(r"!\[P([0-3]) Badge\]")
_TITLE = re.compile(r"!\[P[0-3] Badge\][^\n]*?(?:</sub>)+\s*(.+?)\s*\*\*")
_STATE = re.compile(r"<!--\s*codex-review-state:v1\s*(\{.*?\})\s*-->", re.DOTALL)
_NO_MAJOR = re.compile(r"(?:\bno|n[’']t find any) major issues", re.IGNORECASE)
_BRANCH_ISSUE = re.compile(r"^[a-z]+/(\d+)-")
_QUEUE_REF = re.compile(r"^(?:refs/heads/)?gh-readonly-queue/[^/]+/pr-(\d+)-[0-9a-f]+$")


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


def _no_major_comment(head_sha: str, issue_comments: list[dict[str, Any]]) -> bool:
    """A "no major issues" comment that names the head commit. Its time proves nothing: a commit
    made before the previous head's review finished and pushed after it is older than the comment."""
    for comment in issue_comments:
        body = comment.get("body") or ""
        if _is_bot(comment) and _NO_MAJOR.search(body) and head_sha[:7] in body:
            return True
    return False


def review_status(
    head_sha: str,
    reviews: list[dict[str, Any]],
    review_comments: list[dict[str, Any]],
    issue_comments: list[dict[str, Any]],
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
        or (state.get("commit_sha") == head_sha and state.get("status") == "completed")
        or _no_major_comment(head_sha, issue_comments)
    )
    if reviewed:
        minor = len(findings)
        tail = f"; {minor} P2/P3 finding(s)" if minor else ""
        return Status("success", _limit(f"Codex reviewed {short}: no P0/P1 findings{tail}"))
    if state.get("commit_sha") == head_sha and state.get("status") == "failed":
        return Status("pending", f"Codex could not review {short}; comment '@codex review'")
    return Status("pending", f"Waiting for the Codex review of {short}")


def linked_issue_status(repo: str, branch: str, closing: list[dict[str, Any]],
                        labels: frozenset[str] = frozenset()) -> Status:
    """``closing`` is GraphQL ``closingIssuesReferences.nodes``: number, state, repository."""
    if NO_ISSUE_LABEL in labels:
        return Status("success", f"'{NO_ISSUE_LABEL}' label: a trivial change without an issue")
    if not closing:
        return Status("failure", "No 'Closes #n': link an issue, or add the 'no-issue' label")
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


def dev_green_status(dev_run: dict[str, Any] | None, dev_red: frozenset[str],
                     labels: frozenset[str], head_checks: list[dict[str, Any]],
                     owner_approved: bool = False) -> Status:
    """``dev_run``: the latest completed push CI run on dev (``conclusion``, ``head_sha``);
    ``dev_red``: its red job names; ``head_checks``: the head's check runs (``name``,
    ``status``, ``conclusion``); ``owner_approved``: an admin set ``dev-red-approved``."""
    if dev_run is None:
        return Status("pending", "No completed push CI run on dev yet")
    at = dev_run["head_sha"][:7]
    if dev_run["conclusion"] == "success":
        return Status("success", f"dev push CI is green at {at}")
    reds = ", ".join(sorted(dev_red)) or dev_run["conclusion"]
    if owner_approved and DEV_APPROVED_LABEL in labels:
        return Status("success", _limit(f"Owner approved merging while dev is red at {at} ({reds})"))
    if DEV_FIX_LABEL not in labels:
        return Status("pending", _limit(f"dev is red at {at} ({reds}); only a '{DEV_FIX_LABEL}' PR merges"))
    checks = [c for c in head_checks if c.get("name") != GATE_JOB]
    added = sorted({c["name"] for c in checks if c.get("conclusion") in RED} - dev_red)
    if added:
        return Status("failure", _limit("Adds red: " + ", ".join(added)))
    if any(c.get("status") != "completed" for c in checks):
        return Status("pending", f"Waiting for this fix's checks; dev is red at {at}")
    fixed = sorted(dev_red & {c["name"] for c in checks if c.get("conclusion") == "success"})
    if not fixed:
        return Status("failure", _limit(f"Fixes none of dev's red at {at}: {reds}"))
    return Status("success", _limit("Fixes dev's red: " + ", ".join(fixed)))


def queue_pull_request(head_ref: str) -> int:
    """The pull request a merge-queue ref belongs to (the last one in the group)."""
    match = _QUEUE_REF.match(head_ref or "")
    if not match:
        raise ValueError(f"not a merge queue ref: {head_ref!r}")
    return int(match.group(1))


def _gh(*args: str) -> str:
    return subprocess.run(["gh", *args], check=True, capture_output=True, text=True, encoding="utf-8",
                          timeout=GH_TIMEOUT_S).stdout


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
        nodes { number state repository { nameWithOwner } }
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


def pick_dev_run(runs: list[dict[str, Any]]) -> dict[str, Any] | None:
    """The newest finished push run on dev that has a verdict. A run cancelled because a newer push
    took its place in the concurrency group, or skipped, says nothing about dev. A run still going
    is not waited on: the merge queue tests dev with the pull request before anything merges."""
    for run in runs:
        if run.get("status") == "completed" and run.get("conclusion") not in ("cancelled", "skipped"):
            return run
    return None


def latest_dev_run(repo: str) -> tuple[dict[str, Any] | None, frozenset[str]]:
    run = pick_dev_run(gh_json(f"repos/{repo}/actions/workflows/ci.yml/runs"
                               "?branch=dev&event=push&status=completed&per_page=20")["workflow_runs"])
    if run is None:
        return None, frozenset()
    out = _gh("api", "--paginate", f"repos/{repo}/actions/runs/{run['id']}/jobs?per_page=100",
              "--jq", ".jobs[] | select(.conclusion != null) | [.name, .conclusion] | @tsv")
    red = {name for name, _, conclusion in (line.partition("\t") for line in out.splitlines())
           if conclusion in RED}
    return run, frozenset(red)


def owner_approved(repo: str, pr: int) -> bool:
    """The last actor who added ``dev-red-approved`` has admin permission on the repository."""
    out = _gh("api", "--paginate", f"repos/{repo}/issues/{pr}/events?per_page=100", "--jq",
              f'.[] | select(.event == "labeled" and .label.name == "{DEV_APPROVED_LABEL}") | .actor.login')
    actors = out.split()
    if not actors:
        return False
    permission = gh_json(f"repos/{repo}/collaborators/{actors[-1]}/permission")["permission"]
    return permission == "admin"


def head_check_runs(repo: str, sha: str) -> list[dict[str, Any]]:
    out = _gh("api", "--paginate", f"repos/{repo}/commits/{sha}/check-runs?per_page=100",
              "--jq", ".check_runs[] | {name, status, conclusion}")
    return [json.loads(line) for line in out.splitlines() if line.strip()]


def judge(
    repo: str, pr: int, dev: tuple[dict[str, Any] | None, frozenset[str]],
) -> tuple[dict[str, Any], dict[str, Status] | None]:
    """The three statuses, or ``None`` when the pull request is no longer open.

    ``--all`` lists first. A pull request that merges before this fetch has just closed its
    issue; posting the resulting red on that head misleads anyone reading the checks.
    """
    pull = gh_json(f"repos/{repo}/pulls/{pr}")
    if pull.get("state") != "open" or pull.get("merged"):
        return pull, None
    head_sha = pull["head"]["sha"]
    labels = frozenset(label["name"] for label in pull["labels"])
    return pull, {
        REVIEW_CONTEXT: review_status(
            head_sha,
            gh_list(f"repos/{repo}/pulls/{pr}/reviews?per_page=100"),
            gh_list(f"repos/{repo}/pulls/{pr}/comments?per_page=100"),
            gh_list(f"repos/{repo}/issues/{pr}/comments?per_page=100"),
        ),
        LINKED_CONTEXT: linked_issue_status(repo, pull["head"]["ref"], closing_references(repo, pr), labels),
        DEV_CONTEXT: dev_green_status(*dev, labels, head_check_runs(repo, head_sha),
                                      DEV_APPROVED_LABEL in labels and owner_approved(repo, pr)),
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Set the merge gate statuses.")
    parser.add_argument("--repo", required=True)
    which = parser.add_mutually_exclusive_group(required=True)
    which.add_argument("--pr", type=int)
    which.add_argument("--all", action="store_true", help="Every open pull request into dev.")
    which.add_argument("--merge-group", metavar="HEAD_REF", help="A merge queue ref; needs --sha.")
    parser.add_argument("--sha", help="The merge queue commit to set the statuses on.")
    parser.add_argument("--target-url", default="")
    parser.add_argument("--dry-run", action="store_true", help="Print the statuses, post nothing.")
    args = parser.parse_args(argv)

    if args.merge_group and not args.sha:
        parser.error("--merge-group needs --sha")
    if args.merge_group:
        numbers = [queue_pull_request(args.merge_group)]
    elif args.pr:
        numbers = [args.pr]
    else:
        numbers = [p["number"] for p in gh_list(f"repos/{args.repo}/pulls?state=open&base=dev&per_page=100")]
    dev = latest_dev_run(args.repo)
    for number in numbers:
        pull, statuses = judge(args.repo, number, dev)
        if statuses is None:
            print(f"#{number} skipped: pull request is no longer open")
            continue
        target_sha = args.sha if args.merge_group else pull["head"]["sha"]
        for context, status in statuses.items():
            print(f"#{number} {context} @ {target_sha[:7]}: {status.state} - {status.description}")
            if not args.dry_run:
                post_status(args.repo, target_sha, context, status, args.target_url or pull["html_url"])
    return 0


if __name__ == "__main__":
    sys.exit(main())
