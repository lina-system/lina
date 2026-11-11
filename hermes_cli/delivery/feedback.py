"""``lina work feedback <owner/repo> <pr>``: what a worker must fix on a pull request, as plain text.

For the pull request's current head only: the unresolved P0/P1 review findings (author, file:line, body) and the
last ``LOG_LINES`` lines of each failed required check's log. The text is meant for
``lina worker send <ID> "$(lina work feedback ...)"`` (quick-issue's ``send --from-review``). Nothing to fix prints
nothing. Only reads: no comment, status, label or thread is touched.
"""
from __future__ import annotations

import re

from hermes_cli import merge_rules as rules
from hermes_cli.codex_review_gate import BLOCKING, finding_priority
from hermes_cli.delivery.followups import _resolved
from hermes_cli.delivery.github import GhError, check_runs, required_contexts, rule_types

LOG_LINES = 80
_ESCAPE = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")


def review_findings(comments: list[dict], head: str, resolved: set[int]) -> list[str]:
    """Thread-opening P0/P1 comments made on ``head`` whose thread is not resolved, oldest first."""
    found = []
    for c in sorted(comments, key=lambda c: int(c.get("id") or 0)):
        if c.get("in_reply_to_id") or c.get("original_commit_id") != head or c.get("id") in resolved:
            continue
        priority = finding_priority(c.get("body") or "")
        if priority not in BLOCKING:
            continue
        where = f"{c.get('path') or '?'}:{c.get('line') or c.get('original_line') or '?'}"
        found.append(f"P{priority} {(c.get('user') or {}).get('login') or '?'} {where}\n{(c.get('body') or '').strip()}")
    return found


def _log_tail(gh, repo: str, run: dict) -> str:
    try:
        # Actions logs carry colour codes, which gh refuses to print without the flag; the worker gets them stripped.
        text = _ESCAPE.sub("", gh.run(["api", "--allow-escape-sequences", f"repos/{repo}/actions/jobs/{run['id']}/logs"]))
    except GhError as exc:
        return f"(no log: {exc}) {run.get('html_url') or ''}".rstrip()
    return "\n".join(text.rstrip("\n").splitlines()[-LOG_LINES:])


def failed_checks(gh, repo: str, head: str, base_branch: str) -> list[str]:
    """Each required check of ``base_branch`` that failed on ``head``: its name and the tail of its log."""
    runs = check_runs(gh, repo, head)
    statuses = rules.latest_statuses(gh.pages(f"repos/{repo}/commits/{head}/statuses"))
    found = []
    for name, app_id in required_contexts(rule_types(gh, repo, base_branch)):
        matching = [r for r in runs if r.get("name") == name
                    and (app_id in (None, -1) or (r.get("app") or {}).get("id") == app_id)]
        run = rules.latest_run(matching)
        if run is not None:
            if rules.classify(run) == "failure":
                found.append(f"Failed required check {name} ({run.get('conclusion')}), last {LOG_LINES} log lines:\n"
                             + _log_tail(gh, repo, run))
            continue
        status = statuses.get(name)
        if status and status.get("state") in ("failure", "error"):
            found.append(f"Failed required check {name} ({status['state']}): {status.get('description') or ''} "
                         f"{status.get('target_url') or ''}".rstrip())
    return found


def feedback(gh, repo: str, number: int) -> str:
    pr = gh.json(f"repos/{repo}/pulls/{number}")
    head = pr["head"]["sha"]
    findings = review_findings(gh.pages(f"repos/{repo}/pulls/{number}/comments"), head, _resolved(gh, repo, number))
    checks = failed_checks(gh, repo, head, pr["base"]["ref"])
    if not findings and not checks:
        return ""
    parts = [f"Pull request {repo}#{number} at head {head[:10]}: fix these, then commit."]
    if findings:
        parts.append(f"Unresolved P0/P1 review findings ({len(findings)}):")
        parts += findings
    parts += checks
    return "\n\n".join(parts) + "\n"
