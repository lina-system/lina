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
    """Creation order is the check run's id: GitHub hands ids out as runs are created, so a rerun that is still
    queued (no start or end time yet) is newer than any run before it. Times are no order: a queued run has none,
    and a rerun can start before an older run ends."""
    conclusion = run.get("conclusion")
    unfinished = run.get("status") not in (None, "completed")
    return (unfinished or conclusion != "cancelled", unfinished or conclusion != "skipped", int(run.get("id") or 0))


def latest_run(runs: list[dict]) -> dict | None:
    """The run that decides one check: the newest one (by creation) that was not cancelled (nor skipped while a
    real result exists); a cancelled run decides only when nothing else ran, and a run still going always counts."""
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


def latest_statuses(statuses: list[dict] | None) -> dict[str, dict]:
    """``{context: newest commit status}`` (the highest id per context), as GitHub shows a head's statuses."""
    latest: dict[str, dict] = {}
    for status in statuses or []:
        context = status.get("context")
        if context not in latest or status.get("id", 0) > latest[context].get("id", 0):
            latest[context] = status
    return latest


def status_matches_app(status: dict, app_id: int | None, runs: list[dict]) -> bool:
    """Match a status creator to an app identified by the same head's check-run API answers."""
    if app_id in (None, -1):
        return True
    login = (status.get("creator") or {}).get("login")
    return any(app.get("id") == app_id and app.get("slug")
               and login == f"{app['slug']}[bot]"
               for app in (run.get("app") or {} for run in runs))


def required_checks(required: list[str | tuple[str, int | None]], runs: list[dict],
                    statuses: list[dict] | None = None) -> Verdict:
    verdict = Verdict()
    if not required:
        verdict.add(FAIL, "no required checks are configured on the base branch")
        return verdict
    for check in required:
        name, app_id = (check, None) if isinstance(check, str) else check
        matching = [r for r in runs if r.get("name") == name
                    and (app_id in (None, -1) or (r.get("app") or {}).get("id") == app_id)]
        legacy = [s for s in statuses or [] if s.get("context") == name and status_matches_app(s, app_id, runs)]
        run = latest_run(matching) or latest_statuses(legacy).get(name)
        if run is None:
            verdict.add(WAIT, f"required check {name}: not reported on the head yet")
            continue
        state = classify(run)
        code = {"success": PASS, "pending": WAIT}.get(state, FAIL)
        verdict.add(code, f"required check {name}: {run.get('conclusion') or run.get('state') or run.get('status')}")
    return verdict



_STATUS_CODE = {"success": PASS, "failure": FAIL, "pending": WAIT}


def review_gate():
    """``hermes_cli.codex_review_gate``, which also sets the required ``Codex review`` status: its recognition of
    a Codex review is the only copy, and it ships in the package, so an installed ``lina`` judges the same way."""
    from hermes_cli import codex_review_gate
    return codex_review_gate


def codex_review(head: str, reviews: list[dict], review_comments: list[dict], issue_comments: list[dict]) -> Verdict:
    """The required ``Codex review`` status's own verdict on ``head`` (``review_status`` of the gate script)."""
    verdict = Verdict()
    status = review_gate().review_status(head, reviews, review_comments, issue_comments)
    verdict.add(_STATUS_CODE.get(status.state, FAIL), status.description)
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
              approval: tuple[bool, str] = (False, ""), head_statuses: list[dict] | None = None) -> Verdict:
    """The dev-green guard. ``dev_ok``/``dev_failed``: the newest finished ``dev`` push CI run with a verdict
    and the names of its red jobs. ``head_statuses``: the head's commit statuses (other integrations); the newest
    per context counts like a check run, so a red one blocks too."""
    verdict = Verdict()
    latest = latest_runs([r for r in head_runs if r.get("status") == "completed"])
    head_fail = {name for name, run in latest.items() if run.get("conclusion") in RED}
    passed = {name for name, run in latest.items() if run.get("conclusion") == "success"}
    gate = review_gate()
    own = {gate.REVIEW_CONTEXT, gate.LINKED_CONTEXT, gate.DEV_CONTEXT}
    for context, status in latest_statuses(head_statuses).items():
        if context in own:  # the gate's own statuses: judged by codex_review / linked_issue / this guard
            continue
        if status.get("state") in ("failure", "error"):
            head_fail.add(context)
        elif status.get("state") == "success":
            passed.add(context)
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
    issue_comments, dev_ok, dev_failed, issues, label_events, owner_login`` and ``base`` (the PR's base branch)
    against ``delivery_branch`` (``work.base``): a pull request into any other branch never merges here."""
    verdict = Verdict()
    if pr.get("state") != "OPEN":
        verdict.add(FAIL, f"#{pr.get('number')} is {str(pr.get('state')).lower()}")
        return verdict
    delivery = pr.get("delivery_branch") or "dev"
    if pr.get("base") != delivery:
        verdict.add(FAIL, f"base is {pr.get('base')}, not {delivery}")
        return verdict
    labels = set(pr.get("labels") or ())
    for part in (required_checks(pr.get("required") or [], pr.get("runs") or [], pr.get("statuses")),
                 codex_review(pr["head"], pr.get("reviews") or [], pr.get("review_comments") or [],
                              pr.get("issue_comments") or []),
                 dev_green(labels, pr.get("runs") or [], bool(pr.get("dev_ok", True)), set(pr.get("dev_failed") or ()),
                           owner_approved(labels, pr.get("label_events") or [], pr.get("owner_login")),
                           pr.get("statuses")),
                 linked_issue(pr["repo"], pr.get("body"), labels, pr.get("head_ref") or "", pr.get("issues") or {})):
        verdict.lines.extend(part.lines)
    return verdict
