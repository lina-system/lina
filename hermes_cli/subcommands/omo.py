"""``hermes omo`` (``lina omo``) subcommand: LINA's pinned omo (install, upgrade, rollback, status) and its OCX
routing through ocx-omo (``lina omo routing plan|apply|check``)."""

from __future__ import annotations

import argparse
import json
import sys


def _status(args) -> int:
    from hermes_cli.lina_omo import render_status, status
    data = status()
    for line in [json.dumps(data, indent=2)] if args.json else render_status(data):
        print(line)
    return 0


def _run(args) -> int:
    from hermes_cli import lina_omo, lina_omo_routing
    actions = {
        "install": lambda: lina_omo_routing.install_step(apply=args.apply),
        "upgrade": lambda: lina_omo.upgrade(version=args.omo_version, integrity=args.integrity),
        "rollback": lambda: lina_omo.rollback(),
        "status": lambda: _status(args),
        "routing": lambda: lina_omo_routing.run_verb(args.verb, args.args),
    }
    return actions[args.omo_action]()


def _cmd_omo(args) -> None:
    from hermes_cli.lina_ocx import OcxError
    if not args.omo_action:
        args.omo_action, args.json = "status", False
    try:
        sys.exit(_run(args))
    except OcxError as exc:
        print(f"omo: {exc}")
        sys.exit(1)


def build_omo_parser(subparsers) -> None:
    """Attach the ``omo`` subcommand to ``subparsers``."""
    parser = subparsers.add_parser(
        "omo", help="LINA's pinned omo: install, upgrade, rollback, status, and its OCX routing",
        description="Installs the pinned omo under $LINA_HOME/vendor/omo with the pinned Node (nothing global; an omo "
                    "installed by hand is left alone) and runs it through $LINA_HOME/bin/omo. `routing` runs ocx-omo "
                    "on the pinned Node with the same arguments and exit codes (check: 0 in step, 10 changes to "
                    "review).")
    sub = parser.add_subparsers(dest="omo_action")
    install = sub.add_parser("install", help="Install the pinned omo and ocx-omo, then run `routing check` and show "
                                             "what `routing apply` would change")
    install.add_argument("--apply", action="store_true", help="also write the reviewed routing into ~/.omo/omo.jsonc")
    upgrade = sub.add_parser("upgrade", help="Install a version beside the current one, check `omo --version`, switch "
                                             "current; a running omo daemon is not restarted")
    upgrade.add_argument("omo_version", nargs="?", metavar="version",  # dest `version` is lina's --version
                         help="omo version (default: the pinned one)")
    upgrade.add_argument("--integrity", help="npm integrity (sha512-...) of a version that is not the pin")
    sub.add_parser("rollback", help="Switch back to the omo version before the last upgrade")
    sub.add_parser("status", help="Pinned and installed versions, current, daemon, routing check (read-only)") \
        .add_argument("--json", action="store_true", help="the same as JSON")
    routing = sub.add_parser("routing", help="ocx-omo plan|apply|check on the pinned Node (apply backs up omo.jsonc)")
    routing.add_argument("verb", choices=("plan", "apply", "check"))
    routing.add_argument("args", nargs=argparse.REMAINDER, help="passed to ocx-omo unchanged (--only, --json, ...)")
    parser.set_defaults(func=_cmd_omo)
