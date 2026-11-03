"""Host-wide external message collection controls (lina#23)."""

from __future__ import annotations

from argparse import Namespace


def external_messages_command(args: Namespace) -> None:
    """Change the root switch, or show only aggregate collection information."""
    from hermes_cli.external_messages import enabled
    from hermes_constants import get_default_hermes_root
    from hermes_cli.config_backend import write_config_key

    action = args.external_messages_command
    if action in {"on", "off"}:
        write_config_key(
            get_default_hermes_root() / "config.yaml",
            "memory.external_messages.enabled", action == "on",
        )
        print(f"External messages: {action}")
        return

    import psycopg
    from hermes_state_store import StoreUnavailableError, external_messages_status

    print(f"External messages: {'on' if enabled() else 'off'}")
    try:
        count, collected_at = external_messages_status()
    except (OSError, psycopg.Error, StoreUnavailableError):
        print("Collection status unavailable.")
        raise SystemExit(1) from None
    print(f"Stored messages: {count}")
    print(f"Last collection: {collected_at or 'never'}")
