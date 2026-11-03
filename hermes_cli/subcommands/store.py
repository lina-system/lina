"""``hermes store`` (``lina store``) subcommand: install, inspect, back up, restore and upgrade the memory store."""

from __future__ import annotations

import json
import sys


def _store():
    from hermes_cli.store import Store, store_dir
    return Store(store_dir())


def _list_backups(store) -> None:
    from hermes_cli.store_backup import MANIFEST, generations
    found = generations(store)
    if not found:
        print("No backups yet. `lina store backup` takes one.")
    for generation in found:
        manifest = json.loads((generation / MANIFEST).read_text(encoding="utf-8-sig"))
        print(f"{generation.name}  {manifest.get('bytes', '?')} bytes  {len(manifest.get('fingerprint', {}))} tables")


def _collect_text(older_than_days) -> None:
    """Reclaim message text no profile's transcript points at any more (``MessageStore.collect_unreferenced``)."""
    from hermes_cli.config import load_config
    from hermes_cli.profiles import list_profiles
    from hermes_state import SessionDB
    from hermes_state_store import DEFAULT_COLLECT_DAYS, collect_session_db_text
    if older_than_days is None:
        older_than_days = float((load_config().get("sessions") or {}).get("message_text_grace_days",
                                                                       DEFAULT_COLLECT_DAYS))
    for profile in list_profiles(lazy_skill_count=True):
        db_path = profile.path / "state.db"
        if not db_path.exists():
            continue
        db = SessionDB(db_path)
        try:
            count = collect_session_db_text(db, older_than_days=older_than_days)
        finally:
            db.close()
        print(f"{profile.name}: reclaimed {count} message texts no session uses (older than {older_than_days:g} days)")


def _run_action(args) -> int:
    from hermes_cli import store as store_mod
    from hermes_cli import store_backup, store_upgrade
    store = _store()
    actions = {
        "install": lambda: store_mod.install(store),
        "backup": lambda: store_backup.backup(store),
        "backups": lambda: _list_backups(store),
        "restore": lambda: store_backup.restore(store, args.generation),
        "upgrade": lambda: store_upgrade.upgrade(store),
        "gc": lambda: _collect_text(args.older_than_days),
    }
    if args.store_action == "status":
        return store_mod.status(store)
    actions[args.store_action]()
    return 0


def _cmd_store(args) -> None:
    from hermes_cli.store import StoreError
    if not args.store_action:
        args.store_parser.print_help()
        sys.exit(1)
    try:
        sys.exit(_run_action(args))
    except StoreError as exc:
        print(f"Store: {exc}")
        sys.exit(1)


def build_store_parser(subparsers) -> None:
    """Attach the ``store`` subcommand to ``subparsers``."""
    parser = subparsers.add_parser(
        "store", help="LINA's memory store: Postgres with pgvector and the Honcho server, in Docker",
        description="One Postgres with pgvector (schemas memory, ctx, ltm, vec; rows separated by agent_id) and "
                    "the Honcho api and deriver built from services/honcho, run in Docker.")
    sub = parser.add_subparsers(dest="store_action")
    sub.add_parser("install", help="Build Honcho, start the database, migrate, start Honcho (upgrades an "
                                   "existing store)")
    sub.add_parser("status", help="Which parts run, and the memory schemas")
    sub.add_parser("backup", help="Back up the whole cluster into a new generation")
    sub.add_parser("backups", help="List the backup generations")
    restore = sub.add_parser("restore", help="Restore a generation into a new volume (the current one is kept)")
    restore.add_argument("generation", nargs="?", help="generation name (default: the newest)")
    gc = sub.add_parser("gc", help="Delete message text no profile's sessions use any more, after a grace period")
    gc.add_argument("--older-than-days", type=float, default=None,
                    help="Keep rows written or re-used more recently (default sessions.message_text_grace_days, 30; "
                         "at least 2)")
    sub.add_parser("upgrade", help="Upgrade Honcho: backup, rehearsal migration, one migration job, smoke test")
    parser.set_defaults(func=_cmd_store, store_parser=parser)
