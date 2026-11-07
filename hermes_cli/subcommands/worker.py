"""``hermes worker`` (``lina worker``) subcommand: hand coding-agent work to a work device and run it there (lina#35)."""

from __future__ import annotations

import argparse
import sys

from hermes_cli.lina_work_settings import ACCESS


def _hub():
    from hermes_cli.worker import settings
    from hermes_cli.worker.hub import Hub
    return Hub(settings.root())


def _request_key(args) -> str:
    """``--key``, else a new key, printed before anything else so the caller can retry with it. On stderr: callers
    of ``omo-task submit`` read the task ID from the first word of stdout."""
    if args.key:
        return args.key
    from hermes_cli.worker.request_keys import new_key
    key = new_key()
    print(f"request key: {key}", file=sys.stderr, flush=True)
    return key


def _submit(args) -> int:
    opts = {"request_key": _request_key(args), "briefs": args.brief, "mode": args.mode, "repo": args.repo,
            "inputs": args.input, "todo": args.todo,
            "host": args.host, "model": args.model, "topic": args.topic, "note": args.note, "at": args.at,
            "every": args.every, "dry_notify": args.dry_notify, "wait": args.wait, "runner": args.runner,
            "card": _card(args), "size": args.size, "access": args.access,
            "labels": [x.strip() for x in args.labels.split(",") if x.strip()] if args.labels else None}
    if args.bot:
        opts["bot"] = args.bot
    return _hub().submit({k: v for k, v in opts.items() if v is not None or k in ("wait",)})


def _card(args) -> dict | None:
    import os
    from hermes_cli.worker.hub import HubError
    if args.board and not args.card:
        raise HubError("--board names the board of --card <id>")
    return {"task": args.card, "board": args.board or os.environ.get("HERMES_KANBAN_BOARD") or None} if args.card else None


def _send(args) -> int:
    key = _request_key(args)
    return _hub().send(args.id, args.message, _card(args), access=args.access, request_key=key)


def _device(args) -> int:
    from hermes_cli.worker import device, settings
    from hermes_cli.worker.protocol import verbs
    info = verbs().get(args.verb)
    code, out = device.dispatch(settings.root(), [args.verb, *args.args], sys.stdin.buffer.read()
                                if info and info.stdin else b"")
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
    "send": _send,
    "wait": lambda a: _hub().wait(_card(a), a.id),
    "status": lambda a: _hub().status(a.id, a.json),
    "attach": lambda a: _hub().status(a.id),
    "card": lambda a: _hub().card(a.id, a.card, a.drop, board=a.board),
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
    from hermes_cli.lina_work_settings import SIZES
    from hermes_cli.worker.report import MODES
    p = sub.add_parser("submit", help="Hand a brief to a free work device (bidding), or queue it")
    p.add_argument("--brief", action="append", required=True, help="brief file; several make one batch (one notice)")
    p.add_argument("--mode", choices=list(MODES), default="ulw",
                   help="ulw (plan, do, verify), research, mass (parallel lanes), mass-research")
    p.add_argument("--access", choices=ACCESS, default="workspace-write", help="access mode within the agent's ceiling")
    p.add_argument("--repo", help="owner/name (cloned into the device's repo cache) or a path on the device; the "
                                  "task works in a fresh worktree on omo/<ID> with push blocked")
    p.add_argument("--input", action="append", default=[], help="a file or folder copied read-only to input/")
    p.add_argument("--todo", help="the to-do this task works on (also its topic)")
    p.add_argument("--host", help="a device from devices.workers instead of bidding")
    p.add_argument("--runner", help="a runner from worker.runners (default: worker.routing on --labels/--size, "
                                    "else worker.default_runner, else omo)")
    p.add_argument("--labels", help="the issue's labels, comma separated (its runner label), for worker.routing")
    p.add_argument("--size", type=lambda v: v.strip().removeprefix("size/"), choices=list(SIZES),
                   help="the issue's size (S, M, L or size/S...), for worker.routing")
    p.add_argument("--model", help="an alias from worker.models.aliases or <provider>/<id>[:<thinking>] "
                                   "(default: the runner's model, else worker.models.defaults by mode)")
    p.add_argument("--topic", help="short name of the work when there is no to-do")
    p.add_argument("--bot", "--agent", dest="bot", help="the agent the end notice goes to (default: this profile)")
    p.add_argument("--note", help="a note repeated at the top of the end notice")
    p.add_argument("--at", help='first run time, "YYYY-MM-DD HH:MM" (local)')
    p.add_argument("--every", help="repeat interval: 30m, 6h, 1d, 7d")
    p.add_argument("--dry-notify", action="store_true", help="print the notice command instead of running it")
    p.add_argument("--wait", nargs="?", type=int, const=1800, help="wait in the foreground (seconds, default 1800)")
    p.add_argument("--session", help=argparse.SUPPRESS)  # the old tool's; the end goes to the agent or the card
    _card_args(p)
    _key_arg(p)


def _key_arg(p) -> None:
    p.add_argument("--key", help="request key: a retry with the same key and arguments is the same request, never a "
                                 "second one (default: a new key, printed first as `request key: <key>`)")


def _card_args(p) -> None:
    p.add_argument("--card", help="the Hermes card this run is handed out from (default: HERMES_KANBAN_TASK of "
                                  "this process, else of the nearest ancestor process that has it)")
    p.add_argument("--board", "--card-board", dest="board", help="that card's board (default: HERMES_KANBAN_BOARD)")


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
    __import__("hermes_cli.subcommands.worker_settings", fromlist=["add_parser"]).add_parser(sub)  # lina#256
    for name, text in (("status", "Run, delivery and verdict state; live: what the agent does now"),
                       ("result", "Fetch report.md, final.md, out/, git.txt and check them against the manifest"),
                       ("receipts", "The append-only record of a task"), ("unschedule", "Switch a schedule off")):
        p = sub.add_parser(name, help=text)
        p.add_argument("id")
        if name == "status":
            p.add_argument("--json", action="store_true")
    sub.add_parser("attach", help="The old tool's attach: the run's status (the hub follows every run)") \
        .add_argument("id")
    card = sub.add_parser("card", help="List, add or drop the cards told when a task's runs end")
    card.add_argument("id")
    change = card.add_mutually_exclusive_group()
    change.add_argument("card", nargs="?", help="card to add")
    change.add_argument("--drop", help="card to remove")
    card.add_argument("--board", help="the added card's board (default: HERMES_KANBAN_BOARD)")
    send = sub.add_parser("send", help="Running: steer into the run; waiting on a question: answer it; ended: the "
                                       "next run in the same session")
    send.add_argument("id")
    send.add_argument("message")
    send.add_argument("--access", choices=ACCESS, help="same or lower access for the next run")
    _card_args(send)
    _key_arg(send)
    wait = sub.add_parser("wait", help="Record the runs to wait on and print the Hermes kanban_schedule tool "
                                       "instruction; call that tool and end the turn")
    wait.add_argument("id", nargs="*", help="the runs to wait on (default: every open run of the card)")
    _card_args(wait)
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
