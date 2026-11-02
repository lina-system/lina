"""``hermes desk`` subcommand: create, adopt and show the shared Desk."""

from __future__ import annotations

import argparse
import json


def cmd_desk(args: argparse.Namespace) -> int:
    from hermes_cli.desk import DeskError, desk_info, init_desk

    if args.desk_action == "init":
        try:
            root = init_desk(args.path, adopt=args.adopt)
        except (DeskError, OSError) as exc:
            print(f"Desk not created: {exc}")
            return 1
        print(f"Desk ready at {root}. Every agent now starts there.")
        return 0
    info = desk_info()
    if getattr(args, "json", False):
        print(json.dumps(info, indent=2))
        return 0
    if info["path"] is None:
        print("No Desk configured. Run `hermes desk init` to create one.")
        return 1
    print(f"Desk: {info['path']}" + ("" if info["exists"] else " (missing)"))
    for name, folder in info["folders"].items():
        print(f"  {name}: {folder}/")
    return 0 if info["exists"] else 1


def build_desk_parser(subparsers) -> None:
    """Attach the ``desk`` subcommand to ``subparsers``."""
    parser = subparsers.add_parser(
        "desk", help="Create or show the shared Desk every agent works in",
        description="The Desk is one folder of your files shared by all agents, laid out as "
            "Inbox, Projects, Areas, Resources and Archive. Its path (desk.path in the default "
            "profile's config.yaml) is every agent's default working directory.")
    sub = parser.add_subparsers(dest="desk_action")
    p_init = sub.add_parser("init", help="Create the Desk (default ~/Desk) or adopt an existing folder")
    p_init.add_argument("--path", default=None, help="Desk folder (default: ~/Desk)")
    p_init.add_argument("--adopt", action="store_true",
                        help="Use an existing non-empty folder, keeping its files and numbered PARA folders")
    p_show = sub.add_parser("show", help="Show the Desk path and folders")
    p_show.add_argument("--json", action="store_true", help="Print the Desk as JSON")
    parser.set_defaults(func=cmd_desk, desk_action="show")
