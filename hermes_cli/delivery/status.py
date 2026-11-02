"""``lina work status``: sprint progress per milestone ("계획 n/m · 후속 남음 k": planned items closed of all, and
the open follow-ups counted apart), open pull requests with their gate state, running jobs."""
from __future__ import annotations

from pathlib import Path

from hermes_cli import merge_rules as rules
from hermes_cli.delivery import settings
from hermes_cli.delivery.followups import LABEL
from hermes_cli.delivery.github import GhError
from hermes_cli.delivery.merge import gate_lines


def progress(issues: list[dict], sprint) -> dict[str, dict]:
    """``{milestone: {planned, done, followups}}`` over the sprint's issues (every issue when no sprint is set)."""
    table: dict[str, dict] = {}
    for issue in issues:
        if issue.get("pull_request") or (sprint is not None and not settings.in_sprint(issue.get("body"), sprint)):
            continue
        name = (issue.get("milestone") or {}).get("title") or "no milestone"
        row = table.setdefault(name, {"planned": 0, "done": 0, "followups": 0})
        labels = {label["name"] for label in issue.get("labels") or []}
        if LABEL in labels:
            row["followups"] += issue.get("state") == "open"
        else:
            row["planned"] += 1
            row["done"] += issue.get("state") == "closed"
    return table


def status(gh, repos: list[str], record, base: Path | None = None, out=print) -> int:
    sprint = settings.sprint(base)
    out(f"sprint: {settings.sprint_label(sprint) if sprint else 'not set (lina work sprint set <S>)'}")
    for repo in repos:
        out(f"{repo}")
        for name, row in sorted(progress(gh.pages(f"repos/{repo}/issues?state=all"), sprint).items()):
            out(f"  {name}: 계획 {row['done']}/{row['planned']} · 후속 남음 {row['followups']}")
        for pr in gh.pages(f"repos/{repo}/pulls?state=open"):
            try:
                code, why = gate_lines(gh, repo, pr["number"], base)
            except (GhError, KeyError, ValueError) as exc:
                code, why = rules.FAIL, f"gates unreadable: {exc}"
            out(f"  PR #{pr['number']} {rules.LABEL[code]} {why} · {pr.get('title', '')}")
    running = [job for job in record.jobs() if job["state"] == "running"]
    out(f"running jobs: {len(running)}")
    for job in running:
        out(f"  {job['id']} {job.get('runner') or '-'} {(job.get('text') or '').strip().splitlines()[0][:80]}")
    return 0
