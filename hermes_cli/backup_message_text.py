"""Carry external transcript bodies alongside SQLite recovery images."""

import os
import shutil
import tempfile
import zipfile
from pathlib import Path

from hermes_state_store import (
    TEXT_EXPORT_NAME,
    agent_id_for,
    export_state_db_texts,
    import_state_db_texts,
)


def export_snapshot_texts(source: Path, snapshot: Path, output: Path) -> None:
    """Export the frozen image's references using the source profile's store identity."""
    if source.name != "state.db":
        return
    # The store API derives the agent from the path. A hard link gives the frozen
    # image that identity without reading the live database or copying it again.
    with tempfile.TemporaryDirectory(dir=snapshot.parent) as work:
        home = Path(work) / "profiles" / agent_id_for(source)
        home.mkdir(parents=True)
        image = home / "state.db"
        os.link(snapshot, image)
        export_state_db_texts(image, output)
    if os.name != "nt":
        output.chmod(0o600)


def restore_snapshot_texts(source: Path, target: Path) -> None:
    """Import an adjacent sidecar after publication; old SQLite-only backups still work."""
    if target.name == "state.db" and (
        source.name == "state.db" or source.name.startswith("state.db.pre-update-emergency-")
    ):
        sidecar = source.with_name(TEXT_EXPORT_NAME)
        if source.name.startswith("state.db.pre-update-emergency-"):
            sidecar = source.with_name(f"{source.name}.{TEXT_EXPORT_NAME}")
        if sidecar.is_file():
            import_state_db_texts(target, sidecar)


def import_zip_texts(zf: zipfile.ZipFile, member: str, target: Path) -> None:
    """Restore the sidecar paired with one archive member, independent of ZIP order."""
    if target.name != "state.db":
        return
    sidecar = str(Path(member).with_name(TEXT_EXPORT_NAME)).replace("\\", "/")
    if sidecar not in zf.namelist():
        return
    with tempfile.TemporaryDirectory(dir=target.parent) as work:
        staged = Path(work) / TEXT_EXPORT_NAME
        with zf.open(sidecar) as src, staged.open("wb") as dst:
            shutil.copyfileobj(src, dst)
        import_state_db_texts(target, staged)


def export_profile_texts(source: Path, staged: Path) -> None:
    """Replace a raw copied profile DB with a WAL-safe image and its external text."""
    from hermes_cli.backup_sqlite import _safe_copy_db
    image = staged / "state.db"
    if not image.exists():
        return
    image.unlink()
    for suffix in ("-wal", "-shm", "-journal"):
        (staged / f"state.db{suffix}").unlink(missing_ok=True)
    if not _safe_copy_db(source / "state.db", image):
        raise OSError(f"Could not snapshot {source / 'state.db'}")
    export_snapshot_texts(source / "state.db", image, staged / TEXT_EXPORT_NAME)
