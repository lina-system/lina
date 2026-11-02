"""Honcho upgrade for LINA's store (lina#11): backup, rehearsal migration, one migration job, smoke test.

The rehearsal restores the fresh backup into a throwaway compose project (its own volume and ports), runs the new
image's migrations and smoke test there, and removes it. Only when it passes does the migration job run once on
the real store, with the api and deriver stopped; the backup taken first is the way back.
"""

from __future__ import annotations

import socket
from dataclasses import replace

from hermes_cli.store import HONCHO_IMAGE, PROJECT, Store, StoreError, build_honcho, ensure_env, preflight
from hermes_cli.store_backup import backup, stamp, unpack

PREVIOUS_IMAGE = "lina-honcho:previous"


def _free_port() -> str:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return str(sock.getsockname()[1])


def rehearsal_store(store: Store) -> Store:
    return replace(store, project=f"{PROJECT}-rehearsal", overrides={
        "LINA_STORE_VOLUME": f"lina-store-rehearsal-{stamp().lower()}",
        "LINA_STORE_PORT": _free_port(), "LINA_HONCHO_PORT": _free_port()})


def rehearse(store: Store, generation) -> None:
    rehearsal = rehearsal_store(store)
    try:
        unpack(rehearsal, generation)
        rehearsal.start_db()
        rehearsal.set_role_passwords()
        rehearsal.migrate()
        rehearsal.compose("up", "-d", "--wait", "honcho-api", what="starting the rehearsal api")
        rehearsal.smoke()
    finally:
        rehearsal.compose("down", "-v", "--remove-orphans", what="removing the rehearsal")


def _build_keeping_previous(store: Store) -> None:
    if store.run(["docker", "image", "inspect", HONCHO_IMAGE], capture=True).returncode == 0:
        store.docker("tag", HONCHO_IMAGE, PREVIOUS_IMAGE, what="keeping the current Honcho image")
    build_honcho(store)


def _put_previous_image_back(store: Store) -> None:
    if store.run(["docker", "image", "inspect", PREVIOUS_IMAGE], capture=True).returncode == 0:
        store.docker("tag", PREVIOUS_IMAGE, HONCHO_IMAGE, what="putting the previous Honcho image back")


def upgrade(store: Store) -> None:
    preflight(store)
    ensure_env(store.directory)
    if "db" not in store.running_services():
        store.start_db()
    store.out("Upgrade 1/4: backup")
    generation = backup(store)
    _build_keeping_previous(store)
    store.out("Upgrade 2/4: rehearsal migration on a copy of the backup")
    try:
        rehearse(store, generation)
    except StoreError as exc:
        _put_previous_image_back(store)
        raise StoreError(f"the rehearsal failed, so the store was not changed: {exc}") from exc
    store.out("Upgrade 3/4: migration job")
    try:
        store.stop_writers()
        store.migrate()
        store.out("Upgrade 4/4: smoke test")
        store.start_writers()
        store.smoke()
    except StoreError as exc:
        raise StoreError(f"{exc}. The backup from before the upgrade is {generation.name}: "
                         f"`lina store restore {generation.name}` puts the store back") from exc
    store.out(f"Upgrade done. The backup from before it is {generation.name}.")
