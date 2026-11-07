"""``hermes services`` (``lina services``) subcommand: list LINA's services, start/stop/restart them."""

from __future__ import annotations

import sys


def _cmd_services(args) -> None:
    from hermes_cli import lina_services
    action = args.services_action or "list"
    if action in ("list", "status"):
        for line in lina_services.list_lines():
            print(line)
        sys.exit(0)
    sys.exit(lina_services.act(action, args.service))


def build_services_parser(subparsers) -> None:
    """Attach the ``services`` subcommand to ``subparsers``."""
    from hermes_cli.lina_services import SERVICES

    parser = subparsers.add_parser(
        "services", help="LINA's services: gateway, memory store, OCX, worker, web",
        description="One line per LINA service: name, label, state. The gateway runs as a launchd/systemd "
                    "service; the memory store runs in the Docker compose project lina-store; OCX (#32), the "
                    "worker (#35, `lina worker serve`) and the web server (#288, the dashboard with the app at "
                    "/app/) run as launchd/systemd services too.")
    sub = parser.add_subparsers(dest="services_action")
    sub.add_parser("list", help="One line per service (the default)")
    sub.add_parser("status", help="Same as list")
    names = [service.name for service in SERVICES]
    for verb in (("start", "Start a service (installs the gateway service when it is missing)"),
                 ("stop", "Stop a service; refused while a service that uses it still runs"),
                 ("restart", "Restart a service; refused while a service that uses it still runs")):
        p = sub.add_parser(verb[0], help=verb[1])
        p.add_argument("service", choices=names)
    parser.set_defaults(func=_cmd_services)
