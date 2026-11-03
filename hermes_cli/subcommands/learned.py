"""Parser builder for the learned command; registration belongs to the caller."""


def _cmd_learned(args) -> None:
    from hermes_cli.learned import main
    raise SystemExit(main(args.learned_args))


def build_learned_parser(subparsers) -> None:
    """Attach learning record collection and review to the parent parser."""
    import argparse
    parser = subparsers.add_parser(
        "learned", help="Collect and review masked learning records",
        description="Collect upstream learning records, fold daily changes, review cards and record decisions. "
                    "Only card --send delivers a message.")
    parser.add_argument("learned_args", nargs=argparse.REMAINDER)
    parser.set_defaults(func=_cmd_learned)
