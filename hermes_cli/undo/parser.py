"""Argument parser shared by the package entry point and LINA registration."""

from __future__ import annotations

import argparse


def build(parser: argparse.ArgumentParser) -> None:
    commands = parser.add_subparsers(dest="undo_command", required=True)
    begin = commands.add_parser("begin", help="checkpoint files and open a pending batch")
    begin.add_argument("--bot", required=True)
    begin.add_argument("--what", required=True)
    begin.add_argument("--path", action="append", required=True, help="file to save; repeat for a batch")
    end = commands.add_parser("end", help="journal changed files and close a pending batch")
    end.add_argument("change_id")
    log = commands.add_parser("log", help="record typed references without object contents")
    log.add_argument("--bot", required=True)
    log.add_argument("--change-id")
    log.add_argument("--what", required=True)
    log.add_argument("--objects", required=True, help="JSON array of file, skill, ledger or outside references")
    log.add_argument("--kind", required=True, choices=("revert", "compensate", "none"))
    log.add_argument("--until", default="")
    log.add_argument("--how", required=True)
    last = commands.add_parser("last", help="show the bot's latest change and abandoned batches")
    last.add_argument("--bot", required=True)
    last.add_argument("--within", default="24h")
    for verb in ("show", "apply"):
        command = commands.add_parser(verb, help=f"{verb} one recorded change")
        command.add_argument("change_id")
