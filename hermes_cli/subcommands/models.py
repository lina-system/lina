"""``hermes models`` (``lina models``) subcommand: LINA's pinned OCX: its models, accounts and usage, and install,
upgrade and rollback of OCX itself."""

from __future__ import annotations

import json
import sys


def _view(args) -> int:
    from hermes_cli.lina_models import query, render
    data = query(args.models_action)
    lines = [json.dumps(data, indent=2)] if args.json else render(args.models_action, data)
    for line in lines:
        print(line)
    return 0


def _run(args) -> int:
    from hermes_cli import lina_ocx, lina_ocx_upgrade
    actions = {
        "install": lambda: lina_ocx.install(),
        "upgrade": lambda: lina_ocx_upgrade.upgrade(version=args.version, integrity=args.integrity),
        "rollback": lambda: lina_ocx_upgrade.rollback(),
    }
    if args.models_action in actions:
        return actions[args.models_action]()
    return _view(args)


def _cmd_models(args) -> None:
    from hermes_cli.lina_ocx import OcxError
    args.models_action = args.models_action or "list"
    args.json = getattr(args, "json", False)
    try:
        sys.exit(_run(args))
    except OcxError as exc:
        print(f"OCX: {exc}")
        sys.exit(1)


def build_models_parser(subparsers) -> None:
    """Attach the ``models`` subcommand to ``subparsers``."""
    parser = subparsers.add_parser(
        "models", help="LINA's pinned OCX: models, accounts, usage; install, upgrade, rollback",
        description="Reads LINA's own OCX (`lina services start ocx`) through `ocx ... --json` and prints a short "
                    "table; --json passes OCX's data through. install/upgrade/rollback manage the pinned OCX "
                    "under $LINA_HOME/vendor/ocx.")
    sub = parser.add_subparsers(dest="models_action")
    for name, text in (("list", "The running proxy's model catalog (the default)"),
                       ("accounts", "Provider accounts and API keys (identifiers masked by OCX)"),
                       ("quota", "Accounts with quota observations (may probe upstream)"),
                       ("status", "Proxy status"), ("usage", "Token usage and estimated cost, last 30 days")):
        sub.add_parser(name, help=text).add_argument("--json", action="store_true", help="OCX's JSON, parsed")
    parser.add_argument("--json", action="store_true", help="OCX's JSON for the default view")
    sub.add_parser("install", help="Install the pinned Node and OCX under $LINA_HOME (nothing global)")
    upgrade = sub.add_parser("upgrade", help="Install a version beside the current one, health-check it on a "
                                             "spare port, back up the data dir, switch and restart")
    upgrade.add_argument("version", nargs="?", help="OCX version (default: the pinned one)")
    upgrade.add_argument("--integrity", help="npm integrity (sha512-...) of a version that is not the pin")
    sub.add_parser("rollback", help="Switch back to the version before the last upgrade and restore its data")
    parser.set_defaults(func=_cmd_models)
