"""Host-wide external-message ingestion on the existing cron ticker (#18)."""
from __future__ import annotations

import json
import logging
import time
from typing import Final

logger = logging.getLogger(__name__)
INGEST_INTERVAL_SECONDS: Final = 300


def maybe_ingest() -> None:
    """Check the root-configured spool once per host interval, never per profile.

    Keep the cadence and successful stat signature under the same host lock so
    sequential gateway processes cannot each ingest an unchanged spool.
    """
    from hermes_cli.config_backend import ConfigBackendUnavailable

    try:
        _ingest_if_due()
    except (Exception, ConfigBackendUnavailable):
        # This chore must not prevent cron jobs from running on a broken spool.
        logger.warning("External-message ingestion failed", exc_info=True)


def _ingest_if_due() -> None:
    from gateway.status import _release_file_lock, _try_acquire_file_lock
    from hermes_cli.external_messages import configured_spool, ingest
    from hermes_constants import get_default_hermes_root

    spool = configured_spool()
    if spool is None:
        return
    root = get_default_hermes_root()
    directory = root / "external_messages"
    directory.mkdir(parents=True, exist_ok=True)
    state_path = directory / "ticker.json"
    with (directory / ".ticker.lock").open("a+", encoding="utf-8") as lock:
        if not _try_acquire_file_lock(lock):
            return
        try:
            try:
                state = json.loads(state_path.read_text(encoding="utf-8-sig"))
            except (FileNotFoundError, json.JSONDecodeError):
                state = {}
            now = time.time()
            if 0 <= now - state.get("checked_at", -INGEST_INTERVAL_SECONDS) < INGEST_INTERVAL_SECONDS:
                return
            state["checked_at"] = now
            state_path.write_text(json.dumps(state), encoding="utf-8")
            try:
                stat = spool.stat()
            except FileNotFoundError:
                return
            signature = [str(spool), stat.st_size, stat.st_mtime_ns]
            if state.get("signature") != signature:
                ingest(spool)
            # Record the pre-ingest stat: an append during ingestion must be
            # observed next time even if it arrived after the reader reached EOF.
            state_path.write_text(
                json.dumps({"checked_at": now, "signature": signature}), encoding="utf-8",
            )
        finally:
            _release_file_lock(lock)
