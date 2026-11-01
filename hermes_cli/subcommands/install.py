"""``hermes install`` (``lina install``) subcommand parser."""

from __future__ import annotations

import sys


def _cmd_install(args) -> None:
    from hermes_cli.helper_install import cmd_install
    sys.exit(cmd_install(args))


def build_install_parser(subparsers) -> None:
    """Attach the ``install`` subcommand to ``subparsers``."""
    parser = subparsers.add_parser(
        "install", help="Install or update LINA's device parts (LINA Helper on a Mac)",
        description="On macOS: install or update LINA Helper, then list the permissions a person still has to "
                    "switch on. Other systems skip it.")
    parser.add_argument("--helper-app", metavar="PATH",
                        help="a signed 'LINA Helper.app' to install (default: an ad-hoc build of this checkout)")
    parser.add_argument("--helper-team", metavar="TEAMID",
                        help="Developer ID team of --helper-app (needed for a first install)")
    parser.set_defaults(func=_cmd_install)
