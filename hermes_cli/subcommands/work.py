"""``lina work next|merge|status|sprint``: the delivery procedure from the core (lina#63)."""
from __future__ import annotations

import json
import sys


def _repos(args) -> list[str]:
    from hermes_cli.delivery import settings
    repos = args.repo or settings.repos()
    if not repos:
        raise SystemExit("lina work: no repository; pass --repo owner/name or set work.repos in config.yaml")
    return repos


def _next(args) -> int:
    from hermes_cli.delivery import next as picker
    from hermes_cli.delivery.github import Gh
    from hermes_cli.delivery.record import WorkerHubRecord
    plan = picker.decide(picker.collect(Gh(), _repos(args), WorkerHubRecord()))
    ok = picker.submit(plan) if args.submit else True
    if args.json:
        print(json.dumps(plan, ensure_ascii=False, indent=1, default=list))
    else:
        print("\n".join(picker.lines_of(plan)))
    return 0 if ok else 1


def _merge(args) -> int:
    from hermes_cli.delivery.github import Gh, GhError
    from hermes_cli.delivery.land import LandError, land
    from hermes_cli.delivery.merge import merge_pr
    try:
        if args.branch:
            if not args.task or len(args.target) != 2 or not args.target[1].isdigit():
                raise SystemExit("usage: lina work merge --branch B --task RUN_ID <title> <issue>")
            return land(Gh(), args.branch, args.task, args.target[0], int(args.target[1]))
        if len(args.target) != 2 or not args.target[1].isdigit():
            raise SystemExit("usage: lina work merge <owner/repo> <pr>")
        return merge_pr(Gh(), args.target[0], int(args.target[1]))
    except (GhError, LandError) as exc:
        print(f"lina work merge: {exc}", file=sys.stderr)
        return 1


def _status(args) -> int:
    from hermes_cli.delivery.github import Gh
    from hermes_cli.delivery.record import WorkerHubRecord
    from hermes_cli.delivery.status import status
    return status(Gh(), _repos(args), WorkerHubRecord())


def _sprint(args) -> int:
    from hermes_cli.delivery import settings
    if args.value:
        try:
            print(f"current sprint: {settings.set_sprint(args.value)}")
        except ValueError as exc:
            print(f"lina work sprint: {exc}", file=sys.stderr)
            return 2
        return 0
    current = settings.sprint()
    print(f"current sprint: {settings.sprint_label(current) if current else 'not set'}")
    return 0


_ACTIONS = {"next": _next, "merge": _merge, "status": _status, "sprint": _sprint}


def _cmd_work(args) -> None:
    if not args.work_action:
        args.work_action, args.repo, args.submit, args.json = "next", None, False, False
    sys.exit(_ACTIONS[args.work_action](args))


def build_work_parser(subparsers) -> None:
    parser = subparsers.add_parser(
        "work", help="Run the delivery procedure: what to open next, the only merge path, sprint progress",
        description="docs/delivery.md from the core. `next` picks the issues to start; only an open dependency or a "
                    "running job on the same contract scope holds a start back;, `merge` merges through the gates, "
                    "`status` shows sprint progress, `sprint set` moves the current sprint (the owner's call).")
    sub = parser.add_subparsers(dest="work_action")
    nxt = sub.add_parser("next", help="List what to open and why the rest waits")
    nxt.add_argument("--repo", action="append", metavar="OWNER/NAME", help="repeatable (default: work.repos)")
    nxt.add_argument("--submit", action="store_true",
                     help="hand each pick to the runner its label names through lina worker (recorded on the work "
                          "card when HERMES_KANBAN_TASK is set)")
    nxt.add_argument("--json", action="store_true")
    merge = sub.add_parser("merge", help="Merge a pull request through the gates (exit 0 merged, 1 refused, 75 wait)")
    merge.add_argument("target", nargs="*", metavar="ARG", help="<owner/repo> <pr>, or with --branch: <title> <issue>")
    merge.add_argument("--branch", help="land this lina worker branch: fetch it, merge dev into it, open the PR")
    merge.add_argument("--task", help="the lina worker run id the branch comes from")
    status = sub.add_parser("status", help="Sprint progress per milestone, open pull requests and running jobs")
    status.add_argument("--repo", action="append", metavar="OWNER/NAME")
    sprint = sub.add_parser("sprint", help="Show the current sprint; `sprint set <S>` moves it")
    sprint.add_argument("verb", nargs="?", choices=("set",))
    sprint.add_argument("value", nargs="?", help="S3 or M0 S3")
    parser.set_defaults(func=_cmd_work)
