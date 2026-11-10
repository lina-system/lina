"""``hermes runners`` (``lina runners``) subcommand: LINA's pinned runners (lina#222)."""

from __future__ import annotations

import json
import sys


def _status(args) -> int:
    from hermes_cli import lina_ocx, lina_runners
    if args.offered:
        from hermes_cli.worker import settings
        rows = lina_runners.node_report(settings.root(lina_ocx.lina_root()))
    else:
        rows = lina_runners.status()
    for line in [json.dumps(rows, indent=2, ensure_ascii=False)] if args.json else lina_runners.render(rows):
        print(line)
    return 0


def _sync(args) -> int:
    from hermes_cli import lina_runner_sync
    action = getattr(args, "sync_action", None)
    if action == "status":
        return lina_runner_sync.status(issues_only=args.issues, as_json=args.json)
    if action == "clear":
        return lina_runner_sync.clear(args.runner, node=args.node, accept=args.accept)
    only = [name.strip() for name in (args.only or "").split(",") if name.strip()] or None
    return lina_runner_sync.sync(plan=args.plan, only=only, as_json=args.json)


def _run(args) -> int:
    from hermes_cli import lina_runners
    actions = {
        "install": lambda: lina_runners.install(args.name),
        "upgrade": lambda: lina_runners.upgrade(args.name, version=args.runner_version, integrity=args.integrity),
        "rollback": lambda: lina_runners.rollback(args.name),
        "status": lambda: _status(args),
        "sync": lambda: _sync(args),
    }
    return actions[args.runners_action]()


def _cmd_runners(args) -> None:
    from hermes_cli.lina_ocx import OcxError
    if not args.runners_action:
        args.runners_action, args.json, args.offered = "status", False, False
    try:
        sys.exit(_run(args))
    except OcxError as exc:
        print(f"runners: {exc}")
        sys.exit(1)


def build_runners_parser(subparsers) -> None:
    """Attach the ``runners`` subcommand to ``subparsers``."""
    parser = subparsers.add_parser(
        "runners", help="LINA's pinned runners (codex, claude, grok, dsh, cursor-agent, agy, ACP adapters)",
        description="Installs each pinned runner under $LINA_HOME/vendor/runners with the installer that owns its "
                    "binary (npm on the pinned Node, or the runner's own release artifact checked against its pinned "
                    "hash) and runs it through $LINA_HOME/bin/runners/<name>. Nothing global is installed and a "
                    "runner installed by hand is only read.")
    sub = parser.add_subparsers(dest="runners_action")
    sub.add_parser("install", help="Install the pinned version of one runner, or of every pinned runner") \
        .add_argument("name", nargs="?")
    upgrade = sub.add_parser("upgrade", help="Install beside the current one, trial `--version`, switch current; "
                                             "switch back when the shim does not answer")
    upgrade.add_argument("name")
    upgrade.add_argument("runner_version", nargs="?", metavar="version", help="default: the pinned one")
    upgrade.add_argument("--integrity", help="npm integrity (sha512-...) of a version that is not the pin")
    sub.add_parser("rollback", help="Switch back to the version before the last upgrade").add_argument("name")
    status = sub.add_parser("status", help="Pinned, installed and person-installed versions, sign-in, readiness")
    status.add_argument("--json", action="store_true", help="the same as JSON")
    status.add_argument("--offered", action="store_true",
                        help="only the runners this node offers (worker.runners), as hello reports them")
    sync = sub.add_parser("sync", help="Once a day: move each followed runner to its source's newest version after a "
                                        "trial on the hub, and let the nodes follow (lina#238)")
    sync.add_argument("--plan", action="store_true", help="only show what a run would do; install and write nothing")
    sync.add_argument("--only", help="comma-separated runners (default: every followed runner)")
    sync.add_argument("--json", action="store_true", help="the run's record as JSON")
    sync_sub = sync.add_subparsers(dest="sync_action")
    sync_status = sync_sub.add_parser("status", help="the hub's pins, holds, the last run, issues and notes")
    sync_status.add_argument("--issues", action="store_true", help="only the issue lines (`- none` when there are none)")
    sync_status.add_argument("--json", action="store_true")
    sync_clear = sync_sub.add_parser("clear", help="clear a runner's holds so it moves again")
    sync_clear.add_argument("runner")
    sync_clear.add_argument("--node", help="only that node's hold")
    sync_clear.add_argument("--accept", metavar="VERSION", help="let this major version move")
    parser.set_defaults(func=_cmd_runners)
