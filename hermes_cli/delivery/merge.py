"""``lina work merge <repo> <pr>``: the only merge path (docs/delivery.md steps 6-8).

``collect_pr`` reads the pull request's current head and everything the rule needs; ``merge_rules.may_merge``
decides. Then a merge commit (``gh pr merge --merge --match-head-commit``), or, when the base branch has a merge
queue, a queue entry for exactly that head and a wait until the queue merged or dropped it. After the merge the
linked issue is closed with its Done when ticked and P2/P3 findings go to follow-up issues. Never ``--admin``,
never a force push, never a manual workflow run.
"""
from __future__ import annotations

import time
from pathlib import Path

from hermes_cli import merge_rules as rules
from hermes_cli.delivery import followups, settings
from hermes_cli.delivery.github import GhError, check_runs, dev_state, merge_queue_on, required_contexts, rule_types

ENQUEUE = """mutation($id:ID!,$head:GitObjectID!){enqueuePullRequest(input:{pullRequestId:$id,expectedHeadOid:$head}){
mergeQueueEntry{state position}}}"""
IN_QUEUE = "query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){" \
           "pullRequest(number:$number){isInMergeQueue state}}}"


def _state(pr: dict) -> str:
    if pr.get("merged") or pr.get("merged_at"):
        return "MERGED"
    return "OPEN" if pr.get("state") == "open" else "CLOSED"


def collect_pr(gh, repo: str, number: int, base: Path | None = None) -> dict:
    pr = gh.json(f"repos/{repo}/pulls/{number}")
    head, delivery = pr["head"]["sha"], settings.base_branch(base)
    # Rules and dev green are read on the delivery branch, never on whatever base the PR names.
    found = rule_types(gh, repo, delivery)
    dev_ok, dev_failed = dev_state(gh, repo, delivery, settings.dev_workflow(base))
    snapshot = {"repo": repo, "number": number, "state": _state(pr), "head": head, "head_ref": pr["head"]["ref"],
                "base": pr["base"]["ref"], "delivery_branch": delivery, "node_id": pr.get("node_id"), "body": pr.get("body") or "",
                "labels": [label["name"] for label in pr.get("labels") or []], "required": required_contexts(found),
                "queue": merge_queue_on(found), "runs": check_runs(gh, repo, head),
                "statuses": gh.json(f"repos/{repo}/commits/{head}/statuses?per_page=100") or [],
                "reviews": gh.pages(f"repos/{repo}/pulls/{number}/reviews"),
                "review_comments": gh.pages(f"repos/{repo}/pulls/{number}/comments"),
                "issue_comments": gh.pages(f"repos/{repo}/issues/{number}/comments"),
                "dev_ok": dev_ok, "dev_failed": sorted(dev_failed), "issues": {}, "label_events": [],
                "owner_login": settings.owner_login(base)}
    if rules.DEV_RED_APPROVED in snapshot["labels"]:
        snapshot["label_events"] = gh.pages(f"repos/{repo}/issues/{number}/timeline")
    for n in rules.closed_refs(repo, snapshot["body"]):
        try:
            issue = gh.json(f"repos/{repo}/issues/{n}")
        except GhError:
            continue
        snapshot["issues"][n] = {"state": str(issue.get("state", "")).upper(), "body": issue.get("body") or "",
                                 "pull_request": issue.get("pull_request")}
    return snapshot


def _wait_queue(gh, repo: str, number: int, out, sleep, timeout_s: float, poll_s: float) -> int:
    owner, name = repo.split("/")
    waited = 0.0
    while True:
        node = gh.graphql(IN_QUEUE, owner=owner, name=name, number=number)["data"]["repository"]["pullRequest"]
        if node.get("state") == "MERGED":
            out(f"#{number} merged by the merge queue")
            return rules.PASS
        if not node.get("isInMergeQueue"):
            out(f"#{number} dropped from the merge queue (state {str(node.get('state')).lower()}); see its checks")
            return rules.FAIL
        if waited >= timeout_s:
            out(f"#{number} still in the merge queue after {int(waited // 60)} min")
            return rules.WAIT
        sleep(poll_s)
        waited += poll_s


def after_merge(gh, repo: str, snapshot: dict, out, base: Path | None = None) -> None:
    for n, issue in snapshot["issues"].items():
        if issue.get("pull_request"):
            continue
        current = gh.json(f"repos/{repo}/issues/{n}")
        body = rules.tick_done_when(current.get("body") or "")
        gh.write("PATCH", f"repos/{repo}/issues/{n}", {"body": body, "state": "closed", "state_reason": "completed"})
        ticked, total = rules.done_when(body)
        out(f"#{n} closed, Done when {ticked}/{total} ticked")
    for line in followups.file(gh, repo, snapshot["number"], snapshot["head"], base):
        out(line)


def merge_pr(gh, repo: str, number: int, *, base: Path | None = None, out=print, sleep=time.sleep,
             queue_timeout_s: float = 90 * 60, poll_s: float = 60) -> int:
    snapshot = collect_pr(gh, repo, number, base)
    verdict = rules.may_merge(snapshot)
    for line in verdict.text():
        out(line)
    if verdict.code != rules.PASS:
        out(f"#{number} not merged: {rules.LABEL[verdict.code].lower()}")
        return verdict.code
    if gh.json(f"repos/{repo}/pulls/{number}")["head"]["sha"] != snapshot["head"]:
        out(f"#{number} not merged: the head moved while the gates were read; run again")
        return rules.WAIT
    head = snapshot["head"]
    if snapshot["queue"]:
        entry = gh.graphql(ENQUEUE, id=snapshot["node_id"], head=head)
        state = (((entry.get("data") or {}).get("enqueuePullRequest") or {}).get("mergeQueueEntry") or {})
        out(f"#{number} queued at head {head[:10]} ({str(state.get('state', 'queued')).lower()}, "
            f"position {state.get('position', '?')})")
        code = _wait_queue(gh, repo, number, out, sleep, queue_timeout_s, poll_s)
        if code != rules.PASS:
            return code
    else:
        gh.run(["pr", "merge", str(number), "-R", repo, "--merge", "--match-head-commit", head])
        if _state(gh.json(f"repos/{repo}/pulls/{number}")) != "MERGED":
            out(f"#{number} not merged: GitHub did not merge it")
            return rules.FAIL
        out(f"#{number} merged at head {head[:10]} as a merge commit")
    after_merge(gh, repo, snapshot, out, base)
    return rules.PASS


def gate_lines(gh, repo: str, number: int, base: Path | None = None) -> tuple[int, str]:
    """``(code, first line that is not a pass)`` for ``lina work status``."""
    verdict = rules.may_merge(collect_pr(gh, repo, number, base))
    first = next((text for code, text in verdict.lines if code != rules.PASS), "every gate passes")
    return verdict.code, first
