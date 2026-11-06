"""``hermes migrate`` subcommand parser."""

from __future__ import annotations


def build_migrate_parser(subparsers) -> None:
    """Attach the ``migrate`` subcommand to ``subparsers``."""
    from hermes_cli.migrate import cmd_migrate, cmd_migrate_xai

    migrate_parser = subparsers.add_parser(
        "migrate", help="Migrate configuration for retired models or deprecated settings",
        description="Diagnose and (optionally) rewrite the active config.yaml to "
            "replace references to retired models or deprecated settings.")
    migrate_subparsers = migrate_parser.add_subparsers(dest="migrate_type")

    migrate_xai = migrate_subparsers.add_parser(
        "xai", help="Migrate xAI models scheduled for retirement on May 15, 2026",
        description="Scan config.yaml for references to xAI models retiring on "
            "May 15, 2026 and, with --apply, rewrite them in-place to the "
            "official replacements per the xAI migration guide. The original "
            "config.yaml is backed up before any rewrite.")
    migrate_xai.add_argument(
        "--apply", action="store_true",
        help="Rewrite config.yaml in-place (default: dry-run, no writes)")
    migrate_xai.add_argument(
        "--no-backup", action="store_true",
        help="Skip the timestamped backup of config.yaml when applying")
    migrate_xai.set_defaults(func=cmd_migrate_xai)

    migrate_relay = migrate_subparsers.add_parser(
        "relay", help="Convert legacy HERMES_NEMO_RELAY_ATIF_*/ATOF_* exporter vars into relay-plugins.toml",
        description="The NeMo Relay cutover stopped reading the legacy exporter variables. Generate "
            "<hermes home>/relay-plugins.toml from them, point HERMES_NEMO_RELAY_PLUGINS_TOML at it, "
            "and comment the legacy lines out. `hermes update` runs this for every profile automatically.")
    migrate_relay.add_argument(
        "--all-profiles", action="store_true",
        help="Migrate the default home and every named profile (what `hermes update` does)")
    migrate_relay.add_argument(
        "--no-validate", action="store_true",
        help="Skip activating the generated file through Relay's validator before writing it")
    migrate_relay.set_defaults(func=_cmd_migrate_relay)
    migrate_parser.set_defaults(func=cmd_migrate)
    _build_memory_parser(migrate_subparsers)


def _build_memory_parser(subparsers) -> None:
    parser = subparsers.add_parser("memory", help="Copy a legacy profile into a new LINA agent and verify it")
    parser.add_argument("--from", dest="from_", required=True, help="Legacy Hermes home (never written)")
    selection = parser.add_mutually_exclusive_group(required=True)
    selection.add_argument("--source-profile", dest="profile", help="One source profile; use default for the root profile")
    selection.add_argument("--all-source-profiles", dest="all_profiles", action="store_true",
                           help="Copy source profiles sequentially after preflight")
    parser.add_argument("--as", dest="agent", required=True, help="New agent name; prefix in all-profile mode")
    parser.add_argument("--home", help="Destination LINA root (defaults to the running home; a different root is refused)")
    parser.add_argument("--work-dir", required=True, help="Retained input copies and migration-report.json")
    parser.add_argument("--honcho-dump", help="Logical dump of old Honcho; never a live service")
    parser.add_argument("--honcho-workspace", help="Assert the workspace selected by the copied profile's honcho.json")
    parser.add_argument("--honcho-python", help="Override the installed store image with an explicit Honcho interpreter")
    parser.add_argument("--spool", help="Copied collector input, events.jsonl or its directory")
    parser.add_argument("--work-db", help="Old work-ledger work.db to copy and filter")
    parser.set_defaults(func=_cmd_migrate_memory)


def _cmd_migrate_memory(args) -> None:
    from hermes_cli.migrate_memory import run
    raise SystemExit(run(args))


def _cmd_migrate_relay(args) -> None:
    from hermes_cli.relay_plugin_migrate import cmd_migrate_relay
    cmd_migrate_relay(args)
