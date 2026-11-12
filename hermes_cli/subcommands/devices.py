"""``hermes devices`` (``lina devices``) subcommand: the work devices ``lina worker`` bids over (lina#37)."""

from __future__ import annotations

import sys


def _cmd_devices(args) -> None:
    from hermes_cli import lina_devices as d
    action = args.devices_action or "list"
    actions = {
        "list": lambda: d.list_devices(as_json=getattr(args, "json", False)),
        "add": lambda: d.add(args.name, args.host, cap=args.cap, fast=args.fast, no_repo=args.no_repo, last=args.last,
                             manual=args.manual, lina=args.lina),
        "remove": lambda: d.remove(args.name),
        "check": lambda: d.check(args.name, run_smoke=args.run_smoke, timeout=args.timeout, as_json=args.json),
        "sign-in": lambda: d.sign_in(node=args.node, runner=args.runner, as_json=args.json),
        "service": lambda: d.service(args.name, args.service, args.action, as_json=args.json),
    }
    if action == "sign-in" and (args.node is None) != (args.runner is None):
        print("lina devices: sign-in takes a node and a runner, or neither", file=sys.stderr)
        sys.exit(2)
    try:
        sys.exit(actions[action]())
    except d.DevicesError as exc:
        print(f"lina devices: {exc}", file=sys.stderr)
        sys.exit(2)


def build_devices_parser(subparsers) -> None:
    """Attach the ``devices`` subcommand to ``subparsers``."""
    parser = subparsers.add_parser(
        "devices", help="The work devices lina worker bids over, with their readiness",
        description="devices.workers in the LINA root's config.yaml. Readiness: ssh reachable, the pinned omo present "
                    "(`lina omo status`), free disk, cap and what runs now, and per runner the pinned and installed version, sign-in and one reason "
                    "when not ready (`lina runners`). `check <name> --run-smoke` runs one tiny "
                    "real task there through lina worker and reports done or failed with the run ID.")
    sub = parser.add_subparsers(dest="devices_action")
    sub.add_parser("list", help="Every device with its readiness").add_argument("--json", action="store_true")
    add = sub.add_parser("add", help="Add or replace a device in devices.workers")
    add.add_argument("name")
    add.add_argument("--host", required=True, help="the ssh alias the hub reaches it by, or `local` for this machine")
    add.add_argument("--cap", type=int, default=1, help="tasks it runs at once (default 1)")
    add.add_argument("--fast", action="store_true", help="preferred on a tie")
    add.add_argument("--no-repo", action="store_true", help="docs and light work only, never a repo")
    add.add_argument("--last", action="store_true", help="only when the others are full")
    add.add_argument("--manual", action="store_true", help="takes work only when named with --host")
    add.add_argument("--lina", help="the lina command on that device (default: lina)")
    sub.add_parser("remove", help="Remove a device from devices.workers").add_argument("name")
    check = sub.add_parser("check", help="Readiness of one device (exit 1 when not ready)")
    check.add_argument("name")
    check.add_argument("--run-smoke", action="store_true",
                       help="also run one tiny real task there through lina worker (exit 1 unless it ends done)")
    check.add_argument("--timeout", type=float, default=1800, help="seconds to wait for the smoke task (default 1800)")
    check.add_argument("--json", action="store_true")
    sign_in = sub.add_parser("sign-in", help="Without arguments: one question per runner a person has to sign in to, "
                                             "with the nodes and how (read-only). With <node> <runner>: start that "
                                             "runner's own sign-in on the node and finish it in the hub's browser "
                                             "(exit 1 unless it ends signed_in)")
    sign_in.add_argument("node", nargs="?")
    sign_in.add_argument("runner", nargs="?")
    sign_in.add_argument("--json", action="store_true")
    service = sub.add_parser("service", help="Status or restart of a device's OCX, worker or model proxy (by its "
                                             "kind: ocx or the node's local magpie service) through the service layer")
    service.add_argument("name")
    service.add_argument("service", choices=("ocx", "worker", "proxy"))
    service.add_argument("action", choices=("status", "restart"))
    service.add_argument("--json", action="store_true")
    parser.set_defaults(func=_cmd_devices)
