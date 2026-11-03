"""Quick snapshot creation, composed by the backup command facade."""

import json
import logging
import os
import shutil
from datetime import UTC
from pathlib import Path
from typing import Optional

logger = logging.getLogger("hermes_cli.backup")


def _create_quick_snapshot_locked(
    label: Optional[str] = None,
    hermes_home: Optional[Path] = None,
    keep: Optional[int] = None,
    max_file_size: Optional[int] = None,
) -> Optional[str]:
    """Create a quick state snapshot of critical files.

    Copies STATE_FILES to a timestamped directory under state-snapshots/.
    Auto-prunes old snapshots beyond the keep limit.

    Args:
        max_file_size: When set, individual files larger than this many bytes
            are skipped (with a printed warning) instead of copied. Used by
            the pre-update safety snapshot so a multi-GB ``state.db`` can
            never stall ``hermes update`` or silently eat disk — the small
            pairing/cron/config files the snapshot exists to protect are
            always captured. ``None`` (default) copies everything, which
            preserves manual ``/snapshot`` and ``hermes backup --quick``
            behavior.

    Returns:
        Snapshot ID (timestamp-based), or None if no files found.
    """
    from hermes_cli.backup import (
        get_hermes_home, _quick_snapshot_root, _format_size, _QUICK_STATE_FILES,
        _snapshot_recovery_hint,
        _prune_quick_snapshots, _QUICK_DEFAULT_KEEP, datetime,
    )
    home = hermes_home
    if home is None:
        home = get_hermes_home()
    root = _quick_snapshot_root(home)

    def _too_large(path: Path, rel_name: str) -> bool:
        """True (and warn) when ``path`` exceeds the max_file_size cap."""
        if max_file_size is None:
            return False
        try:
            size = path.stat().st_size
        except OSError:
            return False
        if size <= max_file_size:
            return False
        print(
            f"  ⚠ Snapshot: skipping {rel_name} "
            f"({_format_size(size)} exceeds {_format_size(max_file_size)} limit)"
        )
        logger.warning(
            "Quick snapshot skipped %s: %d bytes exceeds %d byte limit",
            rel_name,
            size,
            max_file_size,
        )
        return True

    ts = datetime.now(UTC).strftime("%Y%m%d-%H%M%S")
    base_snap_id = f"{ts}-{label}" if label else ts
    snap_id = base_snap_id
    suffix = 2
    while (root / snap_id).exists():
        snap_id = f"{base_snap_id}-{suffix}"
        suffix += 1
    snap_dir = root / snap_id
    staging_dir = root / f".{snap_id}.{os.getpid()}.partial"
    shutil.rmtree(staging_dir, ignore_errors=True)
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    if os.name != "nt":
        os.chmod(root, 0o700)
    staging_dir.mkdir(mode=0o700, exist_ok=False)
    logger.info("quick snapshot phase=copy status=started id=%s", snap_id)

    manifest: dict[str, int] = {}  # rel_path -> file size
    failed_dbs: list[str] = []  # present *.db that could not be snapshotted
    # #68805: track protected DB files skipped for size — they are snapshot
    # incompleteness just like a failed copy, so pruning must be suppressed
    # to preserve the older complete snapshot that may contain the only
    # recoverable database.
    oversized_skipped: list[str] = []

    for src, rel in _snapshot_files(home, _QUICK_STATE_FILES):
        if _too_large(src, rel):
            if src.suffix == ".db":
                oversized_skipped.append(rel)
            continue
        dst = staging_dir / rel
        dst.parent.mkdir(parents=True, exist_ok=True)
        if _copy_snapshot_file(src, dst, rel, failed_dbs):
            manifest[rel] = dst.stat().st_size

    from hermes_cli.backup_message_text import export_snapshot_texts, TEXT_EXPORT_NAME
    message_text = None
    if "state.db" in manifest:
        # Not a profile file: restore reads it beside the snapshot's state.db, never copies it into the home,
        # so it stays out of ``files`` (a restored sidecar would count as a restored file).
        export_snapshot_texts(home / "state.db", staging_dir / "state.db", staging_dir / TEXT_EXPORT_NAME)
        message_text = (staging_dir / TEXT_EXPORT_NAME).stat().st_size

    if failed_dbs:
        # Critical: update path used to log-and-continue with exit 0, so a
        # missing state.db backup looked like a successful pre-update snapshot
        # (#68474). Surface this on stdout where operators actually look.
        print(
            "  ⚠ CRITICAL: could not snapshot DB file(s): "
            + ", ".join(failed_dbs)
        )
        print(
            f"  ⚠ If sessions disappear after the update, check {root}. {_snapshot_recovery_hint()}"
        )
        logger.error(
            "Quick snapshot failed to capture DB file(s): %s",
            ", ".join(failed_dbs),
        )

    if not manifest:
        shutil.rmtree(staging_dir, ignore_errors=True)
        if failed_dbs:
            # Distinguish "nothing to snapshot" from "state.db present but unreadable"
            print(
                "  ⚠ Snapshot aborted: no files captured "
                f"(failed DBs: {', '.join(failed_dbs)})"
            )
        return None

    # Write manifest
    meta = {
        "id": snap_id,
        "timestamp": ts,
        "label": label,
        "file_count": len(manifest),
        "total_size": sum(manifest.values()),
        "files": manifest,
        "message_text_bytes": message_text,
        "failed_dbs": failed_dbs,
        "oversized_skipped": oversized_skipped,
    }
    with open(staging_dir / "manifest.json", "w", encoding="utf-8") as f:
        json.dump(meta, f, indent=2)

    # Make the staged quick snapshot owner-only before it is published. The
    # staging directory is private from creation, so copied source modes can
    # be normalized safely before the final atomic rename exposes the
    # snapshot. Permission failures are intentionally fatal: publishing a
    # readable recovery bundle is worse than reporting a failed snapshot.
    _secure_snapshot(root, staging_dir)

    os.replace(staging_dir, snap_dir)

    # Auto-prune. Defaults preserve historical manual /snapshot behavior; callers
    # with known high-churn safety snapshots (for example pre-update) can pass a
    # smaller keep value so large state.db copies do not accumulate indefinitely.
    # #68805 review: skip pruning when a present DB failed to capture OR was
    # skipped for size — either way the snapshot is incomplete and the older
    # snapshot may contain the only recoverable database.
    incomplete = failed_dbs or oversized_skipped
    if not incomplete:
        _prune_quick_snapshots(root, keep=_QUICK_DEFAULT_KEEP if keep is None else keep)
    else:
        if oversized_skipped:
            print(
                "  ⚠ Skipping snapshot prune: DB file(s) skipped for size: "
                + ", ".join(oversized_skipped)
            )
            logger.warning(
                "Quick snapshot skipped oversized DB file(s): %s",
                ", ".join(oversized_skipped),
            )
        logger.warning(
            "Skipping snapshot prune because %d DB(s) failed to capture "
            "and/or %d were oversized — preserving older snapshots as "
            "recovery source",
            len(failed_dbs), len(oversized_skipped),
        )

    logger.info(
        "quick snapshot phase=copy status=complete id=%s files=%d bytes=%d",
        snap_id,
        len(manifest),
        sum(manifest.values()),
    )
    return snap_id


def _snapshot_files(home: Path, entries: tuple[str, ...]):
    """Yield selected regular files, excluding regenerable board workspaces."""
    for rel in entries:
        src = home / rel
        if src.is_file():
            yield src, rel
        elif src.is_dir():
            for sub in src.rglob("*"):
                sub_rel = sub.relative_to(home).as_posix()
                if sub.is_file() and "/workspaces/" not in f"/{sub_rel}/" and "/attachments/" not in f"/{sub_rel}/":
                    yield sub, sub_rel


def _secure_snapshot(root: Path, staging_dir: Path) -> None:
    """Keep recovery bundles owner-only before publishing them."""
    if os.name != "nt":
        os.chmod(root, 0o700)
        os.chmod(staging_dir, 0o700)
        for path in staging_dir.rglob("*"):
            if path.is_dir():
                os.chmod(path, 0o700)
            elif path.is_file():
                os.chmod(path, 0o600)


def _copy_snapshot_file(src: Path, dst: Path, rel: str, failed_dbs: list[str]) -> bool:
    """Copy one image, recording refused SQLite copies for retention policy."""
    from hermes_cli.backup import _safe_copy_db, is_zeroed_sqlite_file
    try:
        if src.suffix == ".db":
            if not _safe_copy_db(src, dst):
                failed_dbs.append(rel)
                print(f"  ⚠ Snapshot: SQLite safe copy FAILED for {rel} — file may be locked or corrupted")
                if is_zeroed_sqlite_file(src):
                    print(f"  ⚠ Snapshot: {rel} looks ZEROED (no SQLite header; {src.stat().st_size} bytes)")
                return False
        else:
            shutil.copy2(src, dst)
        return True
    except (OSError, PermissionError) as exc:
        logger.warning("Could not snapshot %s: %s", rel, exc)
        return False

