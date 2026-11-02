"""``hermes install`` (``lina install``) subcommand parser."""

from __future__ import annotations

import sys


def _start_ocx() -> int:
    from hermes_cli.lina_services import act
    try:
        return act("start", "ocx")
    except SystemExit as exc:
        return exc.code if isinstance(exc.code, int) else 1


def _cmd_install(args) -> None:
    from hermes_cli.helper_install import cmd_install
    from hermes_cli.lina_ocx import install_step as ocx_install_step
    from hermes_cli.lina_services import install_gateway_step
    from hermes_cli.store import install_step
    helper = cmd_install(args)
    store = install_step()
    if store:
        sys.exit(max(helper, store))
    ocx = ocx_install_step()
    if ocx:
        sys.exit(max(helper, ocx))
    if args.services:
        ocx = _start_ocx()
    sys.exit(max(helper, ocx, install_gateway_step(services=args.services)))


def build_install_parser(subparsers) -> None:
    """Attach the ``install`` subcommand, and ``store``, ``services`` and ``models`` (what install sets up), to
    ``subparsers``."""
    from hermes_cli.subcommands.models import build_models_parser
    from hermes_cli.subcommands.services import build_services_parser
    from hermes_cli.subcommands.store import build_store_parser

    parser = subparsers.add_parser(
        "install", help="Install or update LINA's device parts, its memory store and its pinned OCX",
        description="On macOS: install or update LINA Helper, then list the permissions a person still has to "
                    "switch on (other systems skip it). Everywhere: install or upgrade the memory store in Docker "
                    "(`lina store install`), then the pinned Node and OCX under $LINA_HOME (`lina models "
                    "install`). With --services, then start the OCX and gateway services.")
    parser.add_argument("--helper-app", metavar="PATH",
                        help="a signed 'LINA Helper.app' to install (default: an ad-hoc build of this checkout)")
    parser.add_argument("--helper-team", metavar="TEAMID",
                        help="Developer ID team of --helper-app (needed for a first install)")
    parser.add_argument("--services", action="store_true",
                        help="after the store and OCX, start the OCX service and install and start the gateway "
                             "service (`lina services start ocx`, `lina services start gateway`)")
    parser.set_defaults(func=_cmd_install)
    build_store_parser(subparsers)
    build_services_parser(subparsers)
    build_models_parser(subparsers)
