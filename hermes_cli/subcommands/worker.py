"""``hermes worker`` (``lina worker``) subcommand: hand coding-agent work to a work device and run it there (lina#35)."""

from __future__ import annotations

import sys


def _hub():
    from hermes_cli.worker import settings
    from hermes_cli.worker.hub import Hub
    return Hub(settings.root())


def _submit(args) -> int:
    opts = {"briefs": args.brief, "mode": args.mode, "repo": args.repo, "inputs": args.input, "todo": args.todo,
            "host": args.host, "model": args.model, "topic": args.topic, "note": args.note, "at": args.at,
            "every": args.every, "dry_notify": args.dry_notify, "wait": args.wait, "runner": args.runner}
    if args.bot:
        opts["bot"] = args.bot
    return _hub().submit({k: v for k, v in opts.items() if v is not None or k in ("wait",)})


def _device(args) -> int:
    from hermes_cli.worker import device, settings
    code, out = device.dispatch(settings.root(), [args.verb, *args.args], sys.stdin.buffer.read()
                                if args.verb in device.STDIN_VERBS else b"")
    sys.stdout.buffer.write(out)
    return code


def _run_one(args) -> int:
    from hermes_cli.worker import device, settings
    device.run_task(settings.root(), args.id, int(args.run))
    return 0


def _serve(args) -> int:
    from hermes_cli.worker.serve import serve
    return serve(once=args.once)


def _notify(args) -> int:
    from hermes_cli.worker import hub, settings
    print(hub.notify(settings.root(), args.id, args.kind, args.run))
    return 0


def _tick(args) -> int:
    from hermes_cli.worker import hub, settings
    hub.tick(settings.root())
    return 0


_ACTIONS = {
    "submit": _submit,
    "send": lambda a: _hub().send(a.id, a.message),
    "status": lambda a: _hub().status(a.id, a.json),
    "result": lambda a: _hub().result(a.id),
    "accept": lambda a: _hub().verdict(a.id, True, a.note),
    "reject": lambda a: _hub().verdict(a.id, False, a.why),
    "cancel": lambda a: _hub().cancel(a.id, a.why or ""),
    "list": lambda a: _hub().list(a.bot, a.all),
    "receipts": lambda a: _hub().receipts(a.id),
    "hosts": lambda a: _hub().hosts(a.runner),
    "schedules": lambda a: _hub().schedules(),
    "unschedule": lambda a: _hub().unschedule(a.id),
    "serve": _serve,
    "_device": _device,
    "_run": _run_one,
    "_notify": _notify,
    "_tick": _tick,
}


def _cmd_worker(args) -> None:
    from hermes_cli.worker.hub import HubError
    from hermes_cli.worker.runners.base import RunnerError
    from hermes_cli.worker.transport import TransportError
    if not args.worker_action:
        args.worker_action, args.bot, args.all = "list", None, False
    try:
        sys.exit(_ACTIONS[args.worker_action](args))
    except (HubError, RunnerError, TransportError) as exc:
        print(f"lina worker: {exc}", file=sys.stderr)
        sys.exit(2)


def _submit_parser(sub) -> None:
    from hermes_cli.worker.report import MODES
    p = sub.add_parser("submit", help="Hand a brief to a free work device (bidding), or queue it")
    p.add_argument("--brief", action="append", required=True, help="brief file; several make one batch (one notice)")
    p.add_argument("--mode", choices=list(MODES), default="ulw",
                   help="ulw (plan, do, verify), research, mass (parallel lanes), mass-research")
    p.add_argument("--repo", help="owner/name (cloned into the device's repo cache) or a path on the device; the "
                                  "task works in a fresh worktree on omo/<ID> with push blocked")
    p.add_argument("--input", action="append", default=[], help="a file or folder copied read-only to input/")
    p.add_argument("--todo", help="the to-do this task works on (also its topic)")
    p.add_argument("--host", help="a device from devices.workers instead of bidding")
    p.add_argument("--runner", help="a runner from worker.runners (default: worker.default_runner, else omo)")
    p.add_argument("--model", help="an alias from worker.models.aliases or <provider>/<id>[:<thinking>] "
                                   "(default: worker.models.defaults by mode)")
    p.add_argument("--topic", help="short name of the work when there is no to-do")
    p.add_argument("--bot", "--agent", dest="bot", help="the agent the end notice goes to (default: this profile)")
    p.add_argument("--note", help="a note repeated at the top of the end notice")
    p.add_argument("--at", help='first run time, "YYYY-MM-DD HH:MM" (local)')
    p.add_argument("--every", help="repeat interval: 30m, 6h, 1d, 7d")
    p.add_argument("--dry-notify", action="store_true", help="print the notice command instead of running it")
    p.add_argument("--wait", nargs="?", type=int, const=1800, help="wait in the foreground (seconds, default 1800)")


def build_worker_parser(subparsers) -> None:
    """Attach the ``worker`` subcommand to ``subparsers``."""
    parser = subparsers.add_parser(
        "worker", help="Hand coding-agent work to a work device and get the result back as files",
        description="The hub picks a free work device from devices.workers by bidding (or queues the task), the "
                    "device's `lina worker serve` (service ai.lina.worker / lina-worker) runs it with a runner "
                    "from worker.runners (omo by default), settles it, hashes the result and reports the end; one "
                    "notice goes to the agent's chat and, from a work card, onto the card. Never poll: wait for it.")
    sub = parser.add_subparsers(dest="worker_action")
    _submit_parser(sub)
    for name, text in (("status", "Run, delivery and verdict state; live: what the agent does now"),
                       ("result", "Fetch report.md, final.md, out/, git.txt and check them against the manifest"),
                       ("receipts", "The append-only record of a task"), ("unschedule", "Switch a schedule off")):
        p = sub.add_parser(name, help=text)
        p.add_argument("id")
        if name == "status":
            p.add_argument("--json", action="store_true")
    send = sub.add_parser("send", help="Running: steer into the run; waiting on a question: answer it; ended: the "
                                       "next run in the same session")
    send.add_argument("id")
    send.add_argument("message")
    cancel = sub.add_parser("cancel", help="Stop the task (the task folder stays); one notice follows")
    cancel.add_argument("id")
    cancel.add_argument("--why")
    accept = sub.add_parser("accept", help="Record that the result passed your check")
    accept.add_argument("id")
    accept.add_argument("--note", default="")
    reject = sub.add_parser("reject", help="Record that it did not (then send or resubmit)")
    reject.add_argument("id")
    reject.add_argument("--why", required=True)
    listing = sub.add_parser("list", help="Tasks of the last day (--all: every task)")
    listing.add_argument("--bot", "--agent", dest="bot")
    listing.add_argument("--all", action="store_true")
    sub.add_parser("hosts", help="Devices, caps, load, readiness and the queue").add_argument("--runner")
    sub.add_parser("schedules", help="Scheduled and recurring submissions")
    sub.add_parser("serve", help="The device service loop (run by ai.lina.worker / lina-worker)") \
        .add_argument("--once", action="store_true", help="one pass, then exit")
    device = sub.add_parser("_device", help="internal: one device verb for the hub (stdin in, one JSON line out)")
    device.add_argument("verb")
    device.add_argument("args", nargs="*")
    run = sub.add_parser("_run", help="internal: one run of a task on this device")
    run.add_argument("id")
    run.add_argument("run")
    notify = sub.add_parser("_notify", help="internal: a device reports the end of a run")
    for name in ("id", "kind", "run"):
        notify.add_argument(name)
    sub.add_parser("_tick", help="internal: one hub tick (queue, schedules, unreported ends)")
    parser.set_defaults(func=_cmd_worker)
