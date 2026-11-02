"""The one rule for "may this pull request head merge", shared by ``lina work merge`` and ``kanban gate prs``.

docs/delivery.md steps 6-7 as pure functions over GitHub API answers (lina-system/lina#63, #109). Nothing here
reads the network or the disk: the callers collect the answers and pass them in, so every case is a table test.

Standard library only: ``kanban_gates.py`` loads this file by path when it runs as a plain script.

* A check is judged by its latest non-cancelled run (``latest_run``): a manual run on the same head cancels the
  pull-request run of the same name, and that cancelled run never outranks the later result; a skipped run never
  outranks a real pass or failure either.
* Required checks: each one green (success, skipped or neutral) on the head.
* Codex review: Codex reviewed this head and left no P0/P1 finding on it; findings on older heads do not count.
* dev green: the latest ``dev`` push CI passed and no check on the head failed or timed out, required or not; or a
  ``dev-red-fix`` pull request on whose head at least one check red on ``dev`` passes, that fails strictly fewer
  checks than ``dev`` and adds none; or the owner approved merging over the red ``dev`` with ``dev-red-approved``
  (``owner_approved``: the label counts only when the owner account added it) and the head adds no red check.
* Linked issue: the body closes an open issue of this repository (the branch number among them), or ``no-issue``.
"""
from __future__ import annotations

import re
from dataclasses import dataclass, field

PASS, FAIL, WAIT = 0, 1, 75
LABEL = {PASS: "PASS", FAIL: "FAIL", WAIT: "WAIT"}
GREEN = frozenset({"success", "skipped", "neutral"})
RED = frozenset({"failure", "timed_out", "cancelled", "action_required", "startup_failure", "stale"})
CODEX = "chatgpt-codex-connector"
DEV_RED_FIX, DEV_RED_APPROVED, NO_ISSUE = "dev-red-fix", "dev-red-approved", "no-issue"

_BOX = re.compile(r"^(\s*[-*]\s+\[)([ xX])(\]\s+)")
_HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
_P01 = re.compile(r"!\[(P0|P1) Badge\]")
_CLOSES = re.compile(r"\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+(?:([\w.-]+/[\w.-]+))?#(\d+)\b", re.IGNORECASE)
_BRANCH = re.compile(r"^[a-z]+/(\d+)-")


def worst(codes) -> int:
    codes = list(codes)
    if FAIL in codes or not codes:
        return FAIL
    return WAIT if WAIT in codes else PASS


@dataclass
class Verdict:
    lines: list[tuple[int, str]] = field(default_factory=list)

    def add(self, code: int, text: str) -> None:
        self.lines.append((code, text))

    @property
    def code(self) -> int:
        return worst(code for code, _ in self.lines)

    def text(self) -> list[str]:
        return [f"{LABEL[code]} {text}" for code, text in self.lines]



def section(body: str | None, heading: str) -> list[str]:
    lines, inside = [], False
    for raw in (body or "").splitlines():
        match = _HEADING.match(raw)
        if match and len(match[1]) <= 2:
            inside = len(match[1]) == 2 and match[2].strip().lower() == heading.lower()
            continue
        if inside:
            lines.append(raw)
    return lines


def done_when(body: str | None) -> tuple[int, int]:
    boxes = [m[2] for m in (_BOX.match(line) for line in section(body, "Done when")) if m]
    return sum(1 for box in boxes if box in "xX"), len(boxes)


def tick_done_when(body: str | None) -> str:
    """The body with every ``## Done when`` box ticked; other sections untouched."""
    out, inside = [], False
    for raw in (body or "").splitlines(keepends=True):
        match = _HEADING.match(raw.rstrip("\r\n"))
        if match and len(match[1]) <= 2:
            inside = len(match[1]) == 2 and match[2].strip().lower() == "done when"
        elif inside:
            raw = _BOX.sub(lambda m: f"{m[1]}x{m[3]}", raw, count=1)
        out.append(raw)
    return "".join(out)



def _rank(run: dict) -> tuple:
    conclusion = run.get("conclusion")
    return (conclusion != "cancelled", conclusion != "skipped",
            run.get("started_at") or run.get("completed_at") or "", run.get("id") or 0)


def latest_run(runs: list[dict]) -> dict | None:
    """The run that decides one check: the latest one that was not cancelled (nor skipped while a real result
    exists); a cancelled run decides only when nothing else ran."""
    return max(runs, key=_rank, default=None)


def latest_runs(runs: list[dict]) -> dict[str, dict]:
    """``{check name: deciding run}`` over a head's check runs (all of them, not GitHub's ``filter=latest``)."""
    by_name: dict[str, list[dict]] = {}
    for run in runs:
        by_name.setdefault(run.get("name") or "", []).append(run)
    return {name: latest_run(group) for name, group in by_name.items()}


def classify(run: dict) -> str:
    if "conclusion" not in run and "state" in run:
        return {"success": "success", "pending": "pending", "failure": "failure"}.get(run["state"], "infra")
    if run.get("status") != "completed":
        return "pending"
    conclusion = run.get("conclusion")
    if conclusion in GREEN:
        return "success"
    return "failure" if conclusion in RED else "infra"


def required_checks(required: list[str], runs: list[dict], statuses: list[dict] | None = None) -> Verdict:
    verdict = Verdict()
    if not required:
        verdict.add(FAIL, "no required checks are configured on the base branch")
        return verdict
    latest = latest_runs(runs)
    legacy = {}
    for status in statuses or []:
        if status.get("context") not in legacy or status.get("id", 0) > legacy[status["context"]].get("id", 0):
            legacy[status.get("context")] = status
    for name in required:
        run = latest.get(name) or legacy.get(name)
        if run is None:
            verdict.add(WAIT, f"required check {name}: not reported on the head yet")
            continue
        state = classify(run)
        code = {"success": PASS, "pending": WAIT}.get(state, FAIL)
        verdict.add(code, f"required check {name}: {run.get('conclusion') or run.get('state') or run.get('status')}")
    return verdict



def _codex(item: dict) -> bool:
    return str((item.get("user") or {}).get("login", "")).startswith(CODEX)


def codex_review(head: str, reviews: list[dict], review_comments: list[dict], issue_comments: list[dict]) -> Verdict:
    verdict = Verdict()
    findings = [c for c in review_comments if _codex(c) and not c.get("in_reply_to_id")
                and (c.get("original_commit_id") or c.get("commit_id")) == head and _P01.search(c.get("body") or "")]
    summary = re.compile(r'"code_review":\{"commit_sha":"' + re.escape(head) + r'"[^}]*"status":"completed"')
    reviewed = any(_codex(r) and r.get("commit_id") == head for r in reviews) or any(
        _codex(c) and summary.search(c.get("body") or "") for c in issue_comments)
    if findings:
        where = ", ".join(f"{c.get('path')}:{c.get('line') or c.get('original_line') or '?'}" for c in findings)
        verdict.add(FAIL, f"Codex left {len(findings)} P0/P1 finding(s) on head {head[:10]}: {where}")
    elif reviewed:
        verdict.add(PASS, f"Codex reviewed head {head[:10]} with no P0/P1")
    else:
        verdict.add(WAIT, f"no Codex review on head {head[:10]} yet")
    return verdict



def owner_approved(labels: set[str], label_events: list[dict], owner_login: str | None) -> tuple[bool, str]:
    """Whether ``dev-red-approved`` stands as the owner's approval: the label is on the pull request and its latest
    ``labeled`` event in the issue timeline was made by ``owner_login`` (``work.owner_login``). Only the owner
    applies that label; anyone else adding it approves nothing."""
    if DEV_RED_APPROVED not in labels:
        return False, "no dev-red-approved label"
    if not owner_login:
        return False, "dev-red-approved cannot be checked: work.owner_login is not set"
    added = [e for e in label_events if e.get("event") == "labeled"
             and (e.get("label") or {}).get("name") == DEV_RED_APPROVED]
    if not added:
        return False, "dev-red-approved has no labeled event in the timeline"
    actor = str(((max(added, key=lambda e: e.get("created_at") or "")).get("actor") or {}).get("login") or "")
    if actor.lower() != owner_login.lower():
        return False, f"dev-red-approved was added by {actor or 'an unknown account'}, not the owner"
    return True, "dev-red-approved added by the owner"


def dev_green(labels: set[str], head_runs: list[dict], dev_ok: bool, dev_failed: set[str],
              approval: tuple[bool, str] = (False, "")) -> Verdict:
    """The dev-green guard. ``dev_ok``/``dev_failed``: the newest finished ``dev`` push CI run with a verdict
    and the names of its red jobs."""
    verdict = Verdict()
    latest = latest_runs([r for r in head_runs if r.get("status") == "completed"])
    head_fail = {name for name, run in latest.items() if run.get("conclusion") in RED}
    passed = {name for name, run in latest.items() if run.get("conclusion") == "success"}
    if not dev_ok and approval[0] and DEV_RED_FIX not in labels:
        new = head_fail - dev_failed
        verdict.add(FAIL if new else PASS, f"dev is red but {approval[1]}; the head adds red checks: "
                    f"{', '.join(sorted(new))}" if new else f"dev is red; merging over it: {approval[1]}")
        return verdict
    if DEV_RED_FIX not in labels:
        if not dev_ok:
            why = f"; {approval[1]}" if DEV_RED_APPROVED in labels else ""
            verdict.add(FAIL, f"dev is red ({', '.join(sorted(dev_failed)) or 'see the dev run'}); "
                              f"only a dev-red-fix pull request merges until dev is green{why}")
        elif head_fail:
            verdict.add(FAIL, f"red checks on the head: {', '.join(sorted(head_fail))}")
        else:
            verdict.add(PASS, "dev green, no red check on the head")
        return verdict
    new = head_fail - dev_failed
    if not dev_ok and not dev_failed & passed:
        verdict.add(FAIL, "dev-red-fix shows none of dev's red checks passing on its head "
                          f"(skipped or missing: {', '.join(sorted(dev_failed))})")
    elif new:
        verdict.add(FAIL, f"dev-red-fix adds red checks: {', '.join(sorted(new))}")
    elif dev_ok:
        verdict.add(FAIL if head_fail else PASS, f"red checks on the head: {', '.join(sorted(head_fail))}"
                    if head_fail else "dev already green, no red check on the head")
    elif not head_fail < dev_failed:
        verdict.add(FAIL, f"dev-red-fix does not clear any of dev's red ({', '.join(sorted(dev_failed))})")
    else:
        verdict.add(PASS, f"dev-red-fix clears {', '.join(sorted(dev_failed - head_fail))}")
    return verdict



def closed_refs(repo: str, body: str | None) -> list[int]:
    """Issue numbers of this repository the body closes (``Closes #n`` or ``Closes owner/repo#n``)."""
    found = []
    for other, number in _CLOSES.findall(body or ""):
        if (not other or other.lower() == repo.lower()) and int(number) not in found:
            found.append(int(number))
    return found


def linked_issue(repo: str, body: str | None, labels: set[str], head_ref: str, issues: dict[int, dict]) -> Verdict:
    """``issues``: ``{number: issue}`` for the numbers ``closed_refs`` names (``state`` OPEN/CLOSED; missing =
    not an issue of this repository)."""
    verdict = Verdict()
    if NO_ISSUE in labels:
        verdict.add(PASS, "no-issue label")
        return verdict
    refs = closed_refs(repo, body)
    branch = _BRANCH.match(head_ref or "")
    open_refs = [n for n in refs if str((issues.get(n) or {}).get("state", "")).upper() == "OPEN"
                 and not (issues.get(n) or {}).get("pull_request")]
    if not refs:
        verdict.add(FAIL, "the body closes no issue (Closes #n) and there is no no-issue label")
    elif not open_refs:
        verdict.add(FAIL, f"no open issue of {repo} among {', '.join(f'#{n}' for n in refs)}")
    elif branch and int(branch[1]) not in refs:
        verdict.add(FAIL, f"the branch names #{branch[1]} but the body does not close it")
    else:
        verdict.add(PASS, f"closes {', '.join(f'#{n}' for n in open_refs)}")
    return verdict



def may_merge(pr: dict) -> Verdict:
    """May this head merge? ``pr`` holds what the collector read, all on the pull request's current head:
    ``repo, number, state, head, head_ref, body, labels, required, runs, statuses, reviews, review_comments,
    issue_comments, dev_ok, dev_failed, issues, label_events, owner_login``."""
    verdict = Verdict()
    if pr.get("state") != "OPEN":
        verdict.add(FAIL, f"#{pr.get('number')} is {str(pr.get('state')).lower()}")
        return verdict
    labels = set(pr.get("labels") or ())
    for part in (required_checks(pr.get("required") or [], pr.get("runs") or [], pr.get("statuses")),
                 codex_review(pr["head"], pr.get("reviews") or [], pr.get("review_comments") or [],
                              pr.get("issue_comments") or []),
                 dev_green(labels, pr.get("runs") or [], bool(pr.get("dev_ok", True)), set(pr.get("dev_failed") or ()),
                           owner_approved(labels, pr.get("label_events") or [], pr.get("owner_login"))),
                 linked_issue(pr["repo"], pr.get("body"), labels, pr.get("head_ref") or "", pr.get("issues") or {})):
        verdict.lines.extend(part.lines)
    return verdict
