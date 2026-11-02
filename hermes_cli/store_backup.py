"""Whole-cluster backup and restore of LINA's store (lina#11).

A generation is ``<store>/backups/<UTC stamp>/``: ``base.tar.gz`` (``pg_basebackup`` of the whole cluster, WAL
included) and ``manifest.json`` (its sha256 and a fingerprint of every table: row count and a digest of the
rows). It is written under ``.partial-<stamp>`` and renamed into place last, so an interrupted backup never shows
up as a generation. Restore checks the sha256 first, unpacks into a new volume, switches the store to it and
compares the fingerprint; the volume it replaced is kept.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
from dataclasses import replace
from datetime import UTC, datetime
from pathlib import Path

from hermes_cli.store import HONCHO_IMAGE, WRITERS, Store, StoreError, read_env, write_env

FORMAT = 1
ARCHIVE = "base.tar.gz"
MANIFEST = "manifest.json"
TABLES_SQL = ("SELECT format('%I.%I', schemaname, tablename) FROM pg_tables "
              "WHERE schemaname NOT IN ('pg_catalog', 'information_schema') ORDER BY 1;")
# Runs in the db service's image with the target volume mounted; refuses a volume that already holds data.
UNPACK = ('set -e; mkdir -p "$PGDATA"; '
          'if [ -n "$(ls -A "$PGDATA")" ]; then echo "the target volume is not empty" >&2; exit 1; fi; '
          f'tar -xzf /backup/{ARCHIVE} -C "$PGDATA"; chown -R postgres:postgres "$PGDATA"; chmod 700 "$PGDATA"')


def stamp(now: datetime | None = None) -> str:
    return (now or datetime.now(UTC)).strftime("%Y%m%dT%H%M%SZ")


def _literal(text: str) -> str:
    return "'" + text.replace("'", "''") + "'"


def fingerprint(store: Store) -> dict[str, list]:
    """``{"schema.table": [rows, digest]}`` for every table in the cluster's ``lina`` database."""
    tables = store.psql(TABLES_SQL, what="listing the tables").split()
    if not tables:
        return {}
    sql = " UNION ALL ".join(
        f"SELECT {_literal(name)}, count(*), coalesce(md5(string_agg(md5(r::text), '' ORDER BY md5(r::text))), '')"
        f" FROM {name} r" for name in tables)
    rows = store.psql(sql + ";", what="fingerprinting the tables")
    return {name: [int(count), digest] for name, count, digest in (line.split("|") for line in rows.splitlines())}


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _fsync_dir(path: Path) -> None:
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def backups_dir(store: Store) -> Path:
    return store.directory / "backups"


def generations(store: Store) -> list[Path]:
    root = backups_dir(store)
    if not root.is_dir():
        return []
    return sorted(p for p in root.iterdir() if p.is_dir() and not p.name.startswith(".") and (p / MANIFEST).exists())


def _dump(store: Store, partial: Path) -> dict:
    prints = fingerprint(store)
    version = store.psql("SELECT current_setting('server_version');", what="reading the server version").strip()
    archive = partial / ARCHIVE
    with archive.open("wb") as fh:
        store.compose("exec", "-T", "db", "pg_basebackup", "-U", "lina", "-D", "-", "-Ft", "-X", "fetch", "-z",
                      what="pg_basebackup", stdout=fh)
        fh.flush()
        os.fsync(fh.fileno())
    return {"format": FORMAT, "file": ARCHIVE, "sha256": _sha256(archive), "bytes": archive.stat().st_size,
            "postgres": version, "volume": store.values()["LINA_STORE_VOLUME"], "fingerprint": prints,
            "honcho_image": _keep_honcho_image(store)}


def backup(store: Store, *, now: datetime | None = None) -> Path:
    """Quiesce Honcho, take one generation, start Honcho again. Returns the generation's directory."""
    if "db" not in store.running_services():
        raise StoreError("the store's database is not running; start it with `lina store install`")
    root = backups_dir(store)
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    # A leftover means a backup was killed after it stopped Honcho; this one starts Honcho again.
    interrupted = False
    for stale in root.glob(".partial-*"):
        shutil.rmtree(stale)
        interrupted = True
    name = stamp(now)
    final = root / name
    if final.exists():
        raise StoreError(f"a generation named {name} already exists")
    partial = root / f".partial-{name}"
    partial.mkdir(mode=0o700)
    stopped = store.stop_writers()
    restart = list(WRITERS) if interrupted else stopped
    try:
        manifest = {**_dump(store, partial), "created": name}
    finally:
        if restart:
            store.compose("up", "-d", "--wait", *restart, what="starting the Honcho api and deriver again")
    (partial / MANIFEST).write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    _fsync_dir(partial)
    os.rename(partial, final)
    _fsync_dir(root)
    store.out(f"Backup {name}: {manifest['bytes']} bytes, {len(manifest['fingerprint'])} tables ({final}).")
    return final


def verify(generation: Path) -> dict:
    """The generation's manifest, or StoreError naming why the generation cannot be used."""
    try:
        manifest = json.loads((generation / MANIFEST).read_text(encoding="utf-8-sig"))
    except (OSError, ValueError) as exc:
        raise StoreError(f"{generation.name} has no readable {MANIFEST}: {exc}") from exc
    if not isinstance(manifest, dict) or manifest.get("format") != FORMAT:
        raise StoreError(f"{generation.name}: {MANIFEST} is not a format {FORMAT} manifest")
    archive = generation / ARCHIVE
    if not archive.exists():
        raise StoreError(f"{generation.name} is incomplete: {ARCHIVE} is missing")
    actual = _sha256(archive)
    if actual != manifest.get("sha256"):
        raise StoreError(f"{generation.name} is corrupted: {ARCHIVE} has sha256 {actual}, "
                         f"the manifest records {manifest.get('sha256')}")
    return manifest


def find_generation(store: Store, name: str | None) -> Path:
    found = generations(store)
    if name is None:
        if not found:
            raise StoreError(f"no backups in {backups_dir(store)}")
        return found[-1]
    match = backups_dir(store) / name
    if match not in found:
        raise StoreError(f"no backup named {name} in {backups_dir(store)}")
    return match


def unpack(store: Store, generation: Path) -> None:
    """Unpack a generation into the store's (new, empty) data volume."""
    store.compose("run", "--rm", "--no-deps", "--entrypoint", "sh", "-v", f"{generation}:/backup:ro", "db",
                  "-c", UNPACK, what="unpacking the backup into a new volume")


def _differences(expected: dict, actual: dict) -> list[str]:
    return sorted(name for name in set(expected) | set(actual) if expected.get(name) != actual.get(name))


def _switch_volume(store: Store, volume: str) -> None:
    write_env(store.env_file, {**read_env(store.env_file), "LINA_STORE_VOLUME": volume})
    store.start_db()


def kept_image_tag(image: str) -> str:
    return f"{HONCHO_IMAGE.split(':')[0]}:id-{image.removeprefix('sha256:')[:12]}"


def _keep_honcho_image(store: Store) -> str | None:
    """Tag the current Honcho image by its id so a restore can run it again. Docker drops an image whose last
    tag moves (the containerd image store), and an upgrade moves ``lina-honcho:local``."""
    image = store.image_id(HONCHO_IMAGE)
    if image:
        store.docker("tag", image, kept_image_tag(image), what="keeping the Honcho image for this backup")
    return image


def _put_back_honcho_image(store: Store, manifest: dict) -> None:
    """Run Honcho on the image the backup was taken with: its schema matches the restored data."""
    image = manifest.get("honcho_image")
    if not image or image == store.image_id(HONCHO_IMAGE):
        return
    if store.image_id(kept_image_tag(image)) != image:
        store.out(f"The Honcho image the backup was taken with ({image[:19]}) is not on this machine; the current "
                  f"{HONCHO_IMAGE} is used.")
        return
    store.docker("tag", image, HONCHO_IMAGE, what="putting back the Honcho image the backup was taken with")
    store.out(f"{HONCHO_IMAGE} is the image the backup was taken with again ({image[:19]}).")


def restore(store: Store, name: str | None = None, *, now: datetime | None = None) -> str:
    """Restore a generation into a new volume and switch the store to it. Returns the volume it replaced."""
    generation = find_generation(store, name)
    manifest = verify(generation)
    volume = f"lina-store-pgdata-{stamp(now).lower()}"
    unpack(replace(store, overrides={**store.overrides, "LINA_STORE_VOLUME": volume}), generation)
    previous = store.values()["LINA_STORE_VOLUME"]
    store.stop_writers()
    _switch_volume(store, volume)
    store.set_role_passwords()
    differ = _differences(manifest["fingerprint"], fingerprint(store))
    if differ:
        _switch_volume(store, previous)
        raise StoreError(f"the restored data differs from {generation.name} in {', '.join(differ)}; the store is "
                         f"back on volume {previous} (the restored copy is in volume {volume})")
    _put_back_honcho_image(store, manifest)
    store.start_writers()
    store.smoke()
    store.out(f"Restored {generation.name} into volume {volume}; every table matches the backup. The data from "
              f"before the restore stays in volume {previous}: `docker volume rm {previous}` removes it.")
    return previous
