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
    if args.services:  # before omo: its routing is set up through the running OCX
        ocx = _start_ocx()
    if args.role == "worker":
        from hermes_cli.lina_omo_routing import install_step as omo_install_step
        omo = omo_install_step(apply=args.apply)
        if omo:
            sys.exit(max(helper, ocx, omo))
    sys.exit(max(helper, ocx, install_gateway_step(services=args.services)))


def build_install_parser(subparsers) -> None:
    """Attach the ``install`` subcommand, and ``store``, ``services``, ``models``, ``omo`` ``worker`` (what install
    sets up), ``work`` and the operations tools (``heart``, ``undo``, ``learned``), to ``subparsers``."""
    from hermes_cli.subcommands.heart import build_heart_parser
    from hermes_cli.subcommands.learned import build_learned_parser
    from hermes_cli.subcommands.undo import build_undo_parser
    from hermes_cli.subcommands.models import build_models_parser
    from hermes_cli.subcommands.omo import build_omo_parser
    from hermes_cli.subcommands.services import build_services_parser
    from hermes_cli.subcommands.store import build_store_parser
    from hermes_cli.subcommands.work import build_work_parser
    from hermes_cli.subcommands.worker import build_worker_parser

    parser = subparsers.add_parser(
        "install", help="Install or update LINA's device parts, its memory store and its pinned OCX",
        description="On macOS: install or update LINA Helper, then list the permissions a person still has to "
                    "switch on (other systems skip it). Everywhere: install or upgrade the memory store in Docker "
                    "(`lina store install`), then the pinned Node and OCX under $LINA_HOME (`lina models "
                    "install`). With --role worker, then the pinned omo and its OCX routing check (`lina omo install`). "
                    "With --services, then start the OCX and gateway services.")
    parser.add_argument("--helper-app", metavar="PATH",
                        help="a signed 'LINA Helper.app' to install (default: an ad-hoc build of this checkout)")
    parser.add_argument("--helper-team", metavar="TEAMID",
                        help="Developer ID team of --helper-app (needed for a first install)")
    parser.add_argument("--services", action="store_true",
                        help="after the store and OCX, start the OCX service and install and start the gateway "
                             "service (`lina services start ocx`, `lina services start gateway`)")
    parser.add_argument("--role", choices=("worker",),
                        help="worker: a work device; also install the pinned omo and check its OCX routing "
                             "(`lina omo install`)")
    parser.add_argument("--apply", action="store_true",
                        help="with --role worker: write the routing changes the check found (default: show them)")
    parser.set_defaults(func=_cmd_install)
    build_store_parser(subparsers)
    build_services_parser(subparsers)
    build_models_parser(subparsers)
    build_omo_parser(subparsers)
    build_worker_parser(subparsers)
    build_work_parser(subparsers)
    build_heart_parser(subparsers)
    build_undo_parser(subparsers)
    build_learned_parser(subparsers)
