"""``hermes memory`` subcommand parser."""

from __future__ import annotations

from typing import Callable

from hermes_cli.subcommands._shared import add_yes_flag


def build_memory_parser(subparsers, *, cmd_memory: Callable) -> None:
    """Attach the ``memory`` subcommand to ``subparsers``."""
    memory_parser = subparsers.add_parser(
        "memory", help="Configure external memory provider",
        description="Set up and manage external memory provider plugins.\n\n"
            "Bundled providers: honcho, retaindb, byterover.\n"
            "Catalog providers (e.g. hindsight, supermemory,\n"
            "mem0, openviking, holographic): hermes plugins install <name>.\n\n"
            "Only one external provider can be active at a time.\n"
            "Built-in memory (MEMORY.md/USER.md) is always active.")
    memory_sub = memory_parser.add_subparsers(dest="memory_command")
    _setup_parser = memory_sub.add_parser(
        "setup", help="Interactive provider selection and configuration")
    _setup_parser.add_argument(
        "provider", nargs="?", default=None,
        help="Provider to configure directly (e.g. honcho), skipping the picker")
    memory_sub.add_parser("status", help="Show current memory provider config")
    memory_sub.add_parser("off", help="Disable external provider (built-in only)")
    _reset_parser = memory_sub.add_parser(
        "reset", help="Erase all built-in memory (MEMORY.md and USER.md)")
    add_yes_flag(_reset_parser)
    _reset_parser.add_argument(
        "--target", choices=["all", "memory", "user"], default="all",
        help="Which store to reset: 'all' (default), 'memory', or 'user'")
    _eval_parser = memory_sub.add_parser(
        "eval-search", help="Top-5 hit rate per source: today's search against the one-model vector search")
    _eval_parser.add_argument("--questions", required=True, help="JSON Lines file of questions (docs/store.md)")
    _eval_parser.add_argument("--honcho-url", default=None,
                              help="Honcho api for the old memory search (default: not run)")
    _eval_parser.add_argument("--json", default=None, help="also write the scores and every ranking here")
    _cache_parser = memory_sub.add_parser(
        "cache-report", help="Prompt-cache hit rate from OCX usage rows (the lina#17 baseline method)")
    _cache_parser.add_argument("--usage", required=True, help="OCX usage.jsonl")
    _cache_parser.add_argument("--from", dest="start", default=None, help="first ingress time, ISO with offset")
    _cache_parser.add_argument("--to", dest="end", default=None, help="end of the window (exclusive), ISO with offset")
    _cache_parser.add_argument("--conversation", action="append", default=None,
                               help="keep only this conversation id (repeatable)")
    _cache_parser.add_argument("--baseline", action="append", type=float, default=None,
                               help="baseline hit rate in percent to compare with (repeatable)")
    _cache_parser.add_argument("--fingerprints", default=None,
                               help="prefix fingerprints (memory.prefix_fingerprints) for exact common prefixes")
    _cache_parser.add_argument("--format", choices=["md", "tsv"], default="md")
    memory_parser.set_defaults(func=cmd_memory)
    from hermes_cli.external_messages import ingest_command
    external = memory_sub.add_parser("external-messages", help="Ingest the collector's append-only spool")
    external_sub = external.add_subparsers(dest="external_messages_command", required=True)
    ingest = external_sub.add_parser("ingest", help="Ingest new complete spool events")
    ingest.add_argument("--spool", default=None, help="Override memory.external_messages.spool")
    ingest.set_defaults(func=ingest_command)
