#!/usr/bin/env python3
"""``hermes kanban gate prs|dev|artifact|review|ask`` -- deterministic work-card gates.

A work card is done by evidence, not by the agent's word (lina-system/lina#107).
Each gate prints one short line per check and exits 0 (pass), 1 (fail) or
75 (still waiting: CI running, review or answer pending).

Standard library and ``gh`` only, and the board is opened read-only, so this
file also runs as ``python3 kanban_gates.py <gate> ...`` against a board made
by today's Hermes. ``gate prs`` reuses ``collect_acceptance`` from the sibling
``kanban_pr_acceptance.py``, itself stdlib + ``gh``.

A card's to-dos are the lines under its ``## To-dos`` heading:
``- owner/repo#n`` (GitHub issue) or ``- [ ] text`` (checklist item).
"""
from __future__ import annotations

import argparse
import importlib
import importlib.util
import json
import os
import re
import sqlite3
import subprocess
import sys
from pathlib import Path

PASS, FAIL, WAIT = 0, 1, 75
_LABEL = {PASS: "PASS", FAIL: "FAIL", WAIT: "WAIT"}
_ISSUE_TODO = re.compile(r"^\s*[-*]\s+([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#([1-9][0-9]*)\s*$")
_CHECK_TODO = re.compile(r"^\s*[-*]\s+\[[ xX]\]\s+\S")
_QUESTION = re.compile(r"^\s*(질문\s*:|Q\s*:)", re.IGNORECASE)
_APPROVAL = re.compile(r"^\s*(승인|approve)", re.IGNORECASE)
_OK_JOB = {"success", "skipped", "neutral"}
# Comment authors the board records for a human: the CLI's profile-less
# fallback, the dashboard, and the default profile. Override with --owner.
DEFAULT_OWNERS = ("user", "dashboard", "default")


class GateError(RuntimeError):
    pass


class Report:
    def __init__(self) -> None:
        self.codes: list[int] = []

    def line(self, code: int, text: str) -> None:
        self.codes.append(code)
        print(f"{_LABEL[code]} {text}")

    def code(self) -> int:
        if FAIL in self.codes or not self.codes:
            return FAIL
        return WAIT if WAIT in self.codes else PASS



def open_board(db: str | None) -> sqlite3.Connection:
    if not db:
        raise GateError("no board: set HERMES_KANBAN_DB or pass --db")
    path = Path(db).expanduser()
    if not path.is_file():
        raise GateError(f"board {path} does not exist")
    conn = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    return conn


def load_card(conn: sqlite3.Connection, card: str | None) -> sqlite3.Row:
    if not card:
        raise GateError("no card: set HERMES_KANBAN_TASK or pass --card")
    row = conn.execute("SELECT id, assignee, status, body FROM tasks WHERE id = ?", (card,)).fetchone()
    if row is None:
        raise GateError(f"card {card} not found on the board")
    return row


def card_todos(body: str | None) -> tuple[list[tuple[str, int]], list[str]]:
    issues, checklist = [], []
    for line in section(body, "To-dos"):
        match = _ISSUE_TODO.match(line)
        if match:
            issues.append((f"{match[1]}/{match[2]}", int(match[3])))
        elif _CHECK_TODO.match(line):
            checklist.append(line.strip())
    return issues, checklist


def _payload(row: sqlite3.Row) -> dict:
    try:
        value = json.loads(row["payload"] or "null")
    except ValueError:
        return {}
    return value if isinstance(value, dict) else {}


def own_names(conn: sqlite3.Connection, card: sqlite3.Row) -> set[str]:
    """Identities of the card's assignee, whose own approvals and answers never count.

    ``request_review`` reassigns ``tasks.assignee`` to the reviewer, so the
    implementer comes from the ``review_requested`` events (a missing
    implementer means the assignee *was* the reviewer) and from every run
    that was not claimed out of ``review``."""
    own: set[str] = set()
    reviewers: set[str] = set()
    for row in conn.execute("SELECT payload FROM task_events WHERE task_id = ? AND kind = 'review_requested'",
                            (card["id"],)):
        payload = _payload(row)
        reviewers.add(payload.get("reviewer") or "")
        own.add(payload.get("implementer") or payload.get("reviewer") or "")
    review_runs = _review_run_ids(conn, card["id"])
    for row in conn.execute("SELECT id, profile, metadata FROM task_runs WHERE task_id = ?", (card["id"],)):
        if row["id"] not in review_runs and not _synthesized_review(row):
            own.add(row["profile"] or "")
    if card["assignee"] and card["assignee"] not in reviewers:
        own.add(card["assignee"])
    own.discard("")
    return own


def _review_run_ids(conn: sqlite3.Connection, card_id: str) -> set[int]:
    rows = conn.execute("SELECT run_id, payload FROM task_events WHERE task_id = ? AND kind = 'claimed'",
                        (card_id,))
    return {row["run_id"] for row in rows if _payload(row).get("source_status") == "review"}


def _synthesized_review(run: sqlite3.Row) -> bool:
    """Run row ``complete_task`` synthesizes when a card is approved out of ``review`` unclaimed."""
    try:
        metadata = json.loads(run["metadata"] or "null")
    except ValueError:
        return False
    return isinstance(metadata, dict) and metadata.get("source_status") == "review"



def gh_json(*args: str):
    try:
        result = subprocess.run(["gh", *args], stdin=subprocess.DEVNULL, capture_output=True, check=False,
                                text=True, encoding="utf-8", errors="replace", timeout=60)
    except (OSError, subprocess.SubprocessError) as exc:
        raise GateError(f"gh could not run ({type(exc).__name__})") from None
    if result.returncode != 0:
        # gh's stderr can carry host/credential details: keep only the endpoint.
        raise GateError(f"gh {args[0]} {args[1] if len(args) > 1 else ''} exited {result.returncode}".strip())
    try:
        return json.loads(result.stdout)
    except ValueError:
        raise GateError("gh returned non-JSON output") from None


_ISSUE_QUERY = """query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){
issue(number:$number){state stateReason body closedByPullRequestsReferences(first:20,includeClosedPrs:true){
nodes{number url merged mergedAt mergeCommit{oid} repository{nameWithOwner}}}}}}"""


def fetch_issue(repo: str, number: int) -> dict:
    owner, name = repo.split("/")
    value = gh_json("api", "graphql", "-f", f"query={_ISSUE_QUERY}", "-f", f"owner={owner}",
                    "-f", f"name={name}", "-F", f"number={number}")
    issue = ((value.get("data") or {}).get("repository") or {}).get("issue")
    if issue is None:
        raise GateError(f"{repo}#{number} is not readable")
    return issue


def merged_prs(issue: dict) -> list[dict]:
    nodes = (issue.get("closedByPullRequestsReferences") or {}).get("nodes") or []
    return [pr for pr in nodes if pr and pr.get("merged")]


def _sibling(name: str):
    try:
        return importlib.import_module(f"hermes_cli.{name}")
    except ImportError:  # run as a plain file: load the stdlib-only sibling by path
        if name in sys.modules:
            return sys.modules[name]
        spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(f"{name}.py"))
        if spec is None or spec.loader is None:
            raise GateError(f"{name}.py is missing beside this file") from None
        module = importlib.util.module_from_spec(spec)
        sys.modules[name] = module
        spec.loader.exec_module(module)
        return module


def _collect_acceptance():
    return _sibling("kanban_pr_acceptance").collect_acceptance


# The merge rule's Done-when reading (lina#63): one copy, shared with ``lina work merge``.
section = _sibling("merge_rules").section
done_when = _sibling("merge_rules").done_when



def check_issue(repo: str, number: int, issue: dict) -> tuple[int, str]:
    ref = f"{repo}#{number}"
    if issue.get("state") != "CLOSED":
        return FAIL, f"{ref}: issue is still open"
    if issue.get("stateReason") == "NOT_PLANNED":
        return FAIL, f"{ref}: closed as not planned"
    prs = merged_prs(issue)
    if not prs:
        return FAIL, f"{ref}: not closed by a merged pull request"
    ticked, total = done_when(issue.get("body"))
    if total == 0:
        return FAIL, f"{ref}: issue body has no Done when checkboxes"
    if ticked < total:
        return FAIL, f"{ref}: Done when {ticked}/{total} ticked"
    collect = _collect_acceptance()
    worst = PASS
    notes = []
    for pr in prs:
        receipt = collect(pr["url"], None, green=_sibling("merge_rules").GREEN)
        cls = receipt.get("classification")
        if not receipt.get("ok"):
            worst = WAIT if cls == "pending" and worst != FAIL else FAIL
            bad = [c["name"] for c in receipt.get("checks", []) if c.get("classification") != "success"]
            notes.append(f"#{pr['number']} checks {cls}" + (f" ({', '.join(bad)})" if bad else ""))
        else:
            notes.append(f"#{pr['number']} checks green")
    return worst, f"{ref}: merged by {', '.join(notes)}; Done when {ticked}/{total}"


def gate_prs(args: argparse.Namespace, conn: sqlite3.Connection, report: Report) -> None:
    card = load_card(conn, args.card)
    issues, checklist = card_todos(card["body"])
    if not issues:
        report.line(FAIL, f"{card['id']}: no owner/repo#n issues under ## To-dos")
        return
    for repo, number in issues:
        try:
            code, text = check_issue(repo, number, fetch_issue(repo, number))
        except GateError as exc:
            code, text = FAIL, f"{repo}#{number}: {exc}"
        report.line(code, text)
    if checklist:
        print(f"note: {len(checklist)} checklist to-do(s) are not checked by gate prs")



def newest_merge(conn: sqlite3.Connection, card_id: str | None, repo: str) -> dict | None:
    card = load_card(conn, card_id)
    newest = None
    for issue_repo, number in card_todos(card["body"])[0]:
        for pr in merged_prs(fetch_issue(issue_repo, number)):
            same_repo = (pr.get("repository") or {}).get("nameWithOwner", "").lower() == repo.lower()
            if same_repo and pr.get("mergeCommit") and (newest is None or pr["mergedAt"] > newest["mergedAt"]):
                newest = pr
    return newest


def _jobs_report(repo: str, run: dict, report: Report) -> None:
    jobs = gh_json("api", f"repos/{repo}/actions/runs/{run['id']}/jobs?per_page=100&filter=latest")["jobs"]
    pending = [j["name"] for j in jobs if j.get("status") != "completed"]
    failed = [j["name"] for j in jobs if j.get("status") == "completed" and j.get("conclusion") not in _OK_JOB]
    name = f"{run.get('name') or run['id']} run {run['id']}"
    if failed:
        report.line(FAIL, f"{name}: failed jobs: {', '.join(failed)}")
    elif pending or run.get("status") != "completed":
        report.line(WAIT, f"{name}: still running" + (f" ({', '.join(pending)})" if pending else ""))
    elif run.get("conclusion") != "success":
        report.line(FAIL, f"{name}: finished {run.get('conclusion')}")
    else:
        report.line(PASS, f"{name}: {len(jobs)} jobs green")


def gate_dev(args: argparse.Namespace, conn: sqlite3.Connection, report: Report) -> None:
    repo, branch = args.repo, args.branch
    merge = newest_merge(conn, args.card, repo)
    if merge is None:
        report.line(FAIL, f"no merged pull request of the card's issues in {repo}")
        return
    oid = merge["mergeCommit"]["oid"]
    runs = gh_json("api", f"repos/{repo}/actions/runs?branch={branch}&event=push&per_page=50")["workflow_runs"]
    if not runs:
        report.line(FAIL, f"no push runs on {repo}@{branch}")
        return
    head = max(runs, key=lambda r: r["created_at"])["head_sha"]
    status = gh_json("api", f"repos/{repo}/compare/{oid}...{head}")["status"]
    if status not in {"identical", "ahead"}:
        report.line(WAIT, f"latest push run on {branch} ({head[:10]}) is before merge {oid[:10]} of #{merge['number']}")
        return
    print(f"{branch} head {head[:10]} includes merge {oid[:10]} of #{merge['number']}")
    for run in sorted((r for r in runs if r["head_sha"] == head), key=lambda r: r["id"]):
        _jobs_report(repo, run, report)



def _non_empty(path: Path) -> bool:
    if path.is_file():
        return path.stat().st_size > 0
    return any(p.is_file() and p.stat().st_size > 0 for p in path.rglob("*"))


def gate_artifact(args: argparse.Namespace, report: Report) -> None:
    path = Path(args.path).expanduser()
    if not path.exists():
        report.line(FAIL, f"{path}: does not exist")
        return
    if not _non_empty(path):
        report.line(FAIL, f"{path}: is empty")
        return
    report.line(PASS, f"{path}: exists and is not empty")
    sys.stdout.flush()
    try:
        result = subprocess.run(args.check, shell=True, stdin=subprocess.DEVNULL, check=False, timeout=args.timeout)
    except subprocess.TimeoutExpired:
        report.line(FAIL, f"check timed out after {args.timeout}s: {args.check}")
        return
    report.line(PASS if result.returncode == 0 else FAIL, f"check exited {result.returncode}: {args.check}")



def _approvals(conn, card, own, owners) -> list[tuple[int, str]]:
    found = []
    review_runs = _review_run_ids(conn, card["id"])
    for run in conn.execute("SELECT id, profile, metadata, ended_at FROM task_runs "
                            "WHERE task_id = ? AND outcome = 'completed'", (card["id"],)):
        if (run["id"] in review_runs or _synthesized_review(run)) and run["profile"] and run["profile"] not in own:
            found.append((run["ended_at"] or 0, f"review run {run['id']} by {run['profile']} completed"))
    for row in conn.execute("SELECT id, author, body, created_at FROM task_comments WHERE task_id = ?",
                            (card["id"],)):
        if row["author"] in owners and row["author"] not in own and _APPROVAL.match(row["body"] or ""):
            found.append((row["created_at"], f"comment {row['id']} by {row['author']} approves"))
    return found


def gate_review(args: argparse.Namespace, conn: sqlite3.Connection, report: Report) -> None:
    card = load_card(conn, args.card)
    own = own_names(conn, card)
    owners = set(args.owner or DEFAULT_OWNERS)
    changes = conn.execute("SELECT MAX(created_at) FROM task_events WHERE task_id = ? AND kind = 'changes_requested'",
                           (card["id"],)).fetchone()[0] or 0
    approvals = [a for a in _approvals(conn, card, own, owners) if a[0] >= changes]
    if approvals:
        report.line(PASS, f"{card['id']}: {max(approvals)[1]} (assignee: {', '.join(sorted(own)) or '-'})")
    elif card["status"] == "review":
        report.line(WAIT, f"{card['id']}: review pending")
    else:
        report.line(FAIL, f"{card['id']}: no approval by anyone other than {', '.join(sorted(own)) or 'the assignee'}")


def gate_ask(args: argparse.Namespace, conn: sqlite3.Connection, report: Report) -> None:
    card = load_card(conn, args.card)
    own = own_names(conn, card)
    comments = conn.execute("SELECT id, author, body FROM task_comments WHERE task_id = ? ORDER BY id",
                            (card["id"],)).fetchall()
    questions = [c for c in comments if c["author"] in own and _QUESTION.match(c["body"] or "")]
    if not questions:
        report.line(PASS, f"{card['id']}: no open questions")
        return
    for question in questions:
        answer = next((c for c in comments if c["id"] > question["id"] and c["author"] not in own), None)
        first = (question["body"] or "").splitlines()[0][:60]
        if answer is None:
            report.line(WAIT, f"comment {question['id']} unanswered: {first}")
        else:
            report.line(PASS, f"comment {question['id']} answered by {answer['author']} in comment {answer['id']}")



def build_parser(prog: str = "kanban gate") -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog=prog, description="Deterministic work-card gates: exit 0 pass, 1 fail, 75 waiting.")
    parser.add_argument("--db", help="Board DB (default: $HERMES_KANBAN_DB)")
    sub = parser.add_subparsers(dest="gate", required=True)
    card = argparse.ArgumentParser(add_help=False)
    card.add_argument("--card", default=os.environ.get("HERMES_KANBAN_TASK"),
                      help="Card id (default: $HERMES_KANBAN_TASK)")
    sub.add_parser("prs", parents=[card], help="Every To-dos issue closed by a merged PR with green required checks")
    dev = sub.add_parser("dev", parents=[card], help="Latest push run on the branch after the card's last merge is green")
    dev.add_argument("repo", metavar="owner/repo")
    dev.add_argument("--branch", default="dev")
    artifact = sub.add_parser("artifact", help="Path exists, is not empty and the check command exits 0")
    artifact.add_argument("path")
    artifact.add_argument("--check", required=True, metavar="CMD", help="Shell command whose exit 0 passes the gate")
    artifact.add_argument("--timeout", type=float, default=1800, metavar="SECONDS",
                          help="Fail the check command after this long (default: 1800)")
    review = sub.add_parser("review", parents=[card], help="Approved by someone other than the card's assignee")
    review.add_argument("--owner", action="append", metavar="NAME",
                        help=f"Comment author that counts as the owner (repeatable; default: {', '.join(DEFAULT_OWNERS)})")
    sub.add_parser("ask", parents=[card], help="Every question the assignee raised has a later answer")
    return parser


_BOARD_GATES = {"prs": gate_prs, "dev": gate_dev, "review": gate_review, "ask": gate_ask}


def main(argv: list[str] | None = None, *, default_db: str | None = None, prog: str = "kanban gate") -> int:
    args = build_parser(prog).parse_args(argv)
    report = Report()
    try:
        if args.gate == "artifact":
            gate_artifact(args, report)
        else:
            conn = open_board(args.db or os.environ.get("HERMES_KANBAN_DB") or default_db)
            try:
                _BOARD_GATES[args.gate](args, conn, report)
            finally:
                conn.close()
    except (GateError, sqlite3.Error, KeyError, TypeError) as exc:
        report.line(FAIL, f"gate {args.gate}: {exc}")
    code = report.code()
    print(f"gate {args.gate}: {_LABEL[code].lower()}")
    return code


def cli_command(args: argparse.Namespace) -> int:
    """``hermes kanban gate`` handler. ``kanban_command`` dispatches it before ``init_db``:
    a gate must neither create nor migrate the board it only reads."""
    from hermes_cli import kanban_db as kb
    return main(args.gate_argv, default_db=str(kb.kanban_db_path()), prog="hermes kanban gate")


if __name__ == "__main__":
    sys.exit(main(prog=Path(sys.argv[0]).name))
