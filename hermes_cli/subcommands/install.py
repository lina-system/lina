"""``hermes install`` (``lina install``) subcommand parser."""

from __future__ import annotations

import sys


def _cmd_install(args) -> None:
    from hermes_cli.helper_install import cmd_install
    from hermes_cli.lina_services import install_gateway_step
    from hermes_cli.store import install_step
    helper = cmd_install(args)
    store = install_step()
    if store:
        sys.exit(max(helper, store))
    sys.exit(max(helper, install_gateway_step(services=args.services)))


def build_install_parser(subparsers) -> None:
    """Attach the ``install`` subcommand, and ``store`` (what install sets up), to ``subparsers``."""
    from hermes_cli.subcommands.services import build_services_parser
    from hermes_cli.subcommands.store import build_store_parser

    parser = subparsers.add_parser(
        "install", help="Install or update LINA's device parts and its memory store",
        description="On macOS: install or update LINA Helper, then list the permissions a person still has to "
                    "switch on (other systems skip it). Everywhere: install or upgrade the memory store in Docker "
                    "(`lina store install`). With --services, then install and start the gateway service.")
    parser.add_argument("--helper-app", metavar="PATH",
                        help="a signed 'LINA Helper.app' to install (default: an ad-hoc build of this checkout)")
    parser.add_argument("--helper-team", metavar="TEAMID",
                        help="Developer ID team of --helper-app (needed for a first install)")
    parser.add_argument("--services", action="store_true",
                        help="after the store, install and start the gateway service (`lina services start gateway`)")
    parser.set_defaults(func=_cmd_install)
    build_store_parser(subparsers)
    build_services_parser(subparsers)
