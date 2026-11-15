"""``lina eval`` subcommand: measure LINA's checks on labeled sets kept in the store."""

from __future__ import annotations

import sys
from pathlib import Path


def _cmd_eval(args) -> None:
    from hermes_cli.korean.eval import run
    sys.exit(run(args))


def build_eval_parser(subparsers) -> None:
    """Attach the ``eval`` subcommand to ``subparsers``."""
    from hermes_cli.korean.eval import DEFAULT_MAX_FALSE_ALARM, SMOKE_BELOW

    parser = subparsers.add_parser("eval", help="Measure LINA's checks on labeled sets kept in the store")
    targets = parser.add_subparsers(dest="eval_target", required=True)
    korean = targets.add_parser(
        "korean", help="Score every registered Korean check on the labeled Korean set (docs/korean.md)",
        description="Per check: flagged sentences caught, flagged phrases caught and false alarms on clean texts, "
                    "each with n and a 95% Wilson interval. Prints counts only, never a text of the set. With fewer "
                    f"than {SMOKE_BELOW} clean texts the run is smoke only.")
    korean.add_argument("--gate", action="store_true",
                        help="exit 1 when a check's false alarms on clean texts may exceed --max-false-alarm (upper "
                             f"95%% bound); refused (exit 2) with fewer than {SMOKE_BELOW} clean texts")
    korean.add_argument("--max-false-alarm", type=float, default=DEFAULT_MAX_FALSE_ALARM, metavar="RATE",
                        help=f"the gate's false-alarm limit, 0-1 (default {DEFAULT_MAX_FALSE_ALARM})")
    korean.add_argument("--save-baseline", metavar="NAME",
                        help="store this run's per-check numbers (counts only) as baseline NAME; never replaces one")
    korean.add_argument("--baseline", metavar="NAME",
                        help="compare with baseline NAME (default: the newest saved baseline)")
    actions = korean.add_subparsers(dest="korean_action")
    importer = actions.add_parser(
        "import", help="Add labeled texts from TSV files to the set in the store",
        description="--flagged: sentence, phrase[, agent[, source]]. --clean: text, marked_by (owner|reviewer)"
                    "[, marker_family[, writer_family[, agent[, source]]]]. One text per line, tab-separated; every "
                    "line is imported, already in the set, or refused with its line number and a reason.")
    importer.add_argument("--flagged", type=Path, metavar="FILE", help="TSV of flagged sentences")
    importer.add_argument("--clean", type=Path, metavar="FILE", help="TSV of texts marked clean")
    importer.add_argument("--source", default="import",
                          help="source label for lines without their own (default: import)")
    parser.set_defaults(func=_cmd_eval)
