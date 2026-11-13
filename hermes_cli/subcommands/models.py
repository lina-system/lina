"""``hermes models`` (``lina models``) subcommand: the models, accounts, quota and usage of this machine's magpie,
read-only."""

from __future__ import annotations

import json
import sys


def _cmd_models(args) -> None:
    from hermes_cli.lina_magpie import MagpieError
    from hermes_cli.lina_models import query, render
    view = args.models_action or "list"
    try:
        data = query(view)
    except MagpieError as exc:
        print(f"unknown: {exc}")
        sys.exit(1)
    for line in [json.dumps(data, indent=2)] if getattr(args, "json", False) else render(view, data):
        print(line)
    sys.exit(0)


def build_models_parser(subparsers) -> None:
    """Attach the ``models`` subcommand to ``subparsers``."""
    parser = subparsers.add_parser(
        "models", help="This machine's magpie: models, accounts, quota, usage (read-only)",
        description="Reads the magpie gateway worker.proxy names (default 127.0.0.1:3425) and prints a short table; "
                    "--json prints the data. LINA never installs, configures or restarts magpie.")
    sub = parser.add_subparsers(dest="models_action")
    for name, text in (("list", "The models magpie lists (the default)"),
                       ("accounts", "Subscription and plan accounts with their quota windows"),
                       ("quota", "The same accounts, as magpie last read their quota"),
                       ("status", "Whether magpie answers, and how many models it lists"),
                       ("usage", "Token usage and cost, last 30 days")):
        sub.add_parser(name, help=text).add_argument("--json", action="store_true", help="The data as JSON")
    parser.add_argument("--json", action="store_true", help="The default view as JSON")
    parser.set_defaults(func=_cmd_models)
