"""The delivery verbs of ``lina work`` (lina#63): ``next``, ``merge``, ``status`` and ``sprint current``.

``hermes_cli/work/parser.py`` (lina#36) owns the one ``lina work`` parser and points these verbs here, as it points
its own verbs at ``hermes_cli/work/cmd_*``. Each verb ends the process with its exit code (0 done or pass,
1 refused or failed, 2 usage, 75 waiting), so the record's ``execute`` neither resumes hand-offs nor recomputes
items for them: they read GitHub and ``lina worker``, never the work record.
"""
from __future__ import annotations

import json
import sys


def _repos(args) -> list[str]:
    from hermes_cli.delivery import settings
    repos = args.repo or settings.repos()
    if not repos:
        print("lina work: no repository; pass --repo owner/name or set work.repos in config.yaml", file=sys.stderr)
        raise SystemExit(2)
    return repos


def cmd_next(args) -> None:
    from hermes_cli.delivery import next as picker
    from hermes_cli.delivery.github import Gh
    from hermes_cli.delivery.record import WorkerHubRecord
    plan = picker.decide(picker.collect(Gh(), _repos(args), WorkerHubRecord()))
    ok = picker.submit(plan) if args.submit else True
    if args.json:
        print(json.dumps(plan, ensure_ascii=False, indent=1, default=list))
    else:
        print("\n".join(picker.lines_of(plan)))
    raise SystemExit(0 if ok else 1)


def _merge_code(args) -> int:
    from hermes_cli.delivery.github import Gh, GhError
    from hermes_cli.delivery.land import LandError, land
    from hermes_cli.delivery.merge import merge_pr
    try:
        if args.branch:
            if not args.task or len(args.target) != 2 or not args.target[1].isdigit():
                print("usage: lina work merge --branch B --task RUN_ID <title> <issue>", file=sys.stderr)
                return 2
            return land(Gh(), args.branch, args.task, args.target[0], int(args.target[1]))
        if len(args.target) != 2 or not args.target[1].isdigit():
            print("usage: lina work merge <owner/repo> <pr>", file=sys.stderr)
            return 2
        return merge_pr(Gh(), args.target[0], int(args.target[1]))
    except (GhError, LandError) as exc:
        print(f"lina work merge: {exc}", file=sys.stderr)
        return 1


def cmd_merge(args) -> None:
    raise SystemExit(_merge_code(args))


def cmd_status(args) -> None:
    from hermes_cli.delivery.github import Gh
    from hermes_cli.delivery.record import WorkerHubRecord
    from hermes_cli.delivery.status import status
    raise SystemExit(status(Gh(), _repos(args), WorkerHubRecord()))


def cmd_sprint_current(args) -> None:
    from hermes_cli.delivery import settings
    if args.value:
        try:
            print(f"current sprint: {settings.set_sprint(args.value)}")
        except ValueError as exc:
            print(f"lina work sprint current: {exc}", file=sys.stderr)
            raise SystemExit(2) from None
        raise SystemExit(0)
    current = settings.sprint()
    print(f"current sprint: {settings.sprint_label(current) if current else 'not set'}")
    raise SystemExit(0)
