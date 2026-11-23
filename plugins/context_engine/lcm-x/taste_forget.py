"""Owner-scoped taste policy and durable tombstone evidence for LCM-X."""

from __future__ import annotations

import hashlib
import re
from collections.abc import Iterator
from contextlib import closing
from pathlib import Path
from typing import Final

from .config import lcm_env_get
from .dag import SummaryDAG
from .sqlite_session import configured_store_backend, store_backend

TASTE_INSTRUCTION: Final = (
    "Do not record the agent's likes or dislikes as lasting traits; describe only what happened."
)
_KEY: Final = "lina_taste_forget:"
_WORD_START: Final = re.compile(r"(?<!\w)(?=\S)")
_WORD_END: Final = re.compile(r"(?:에서|에게|으로|까지|부터|처럼|은|는|이|가|을|를|의|에|로|와|과|도|만)?(?!\w)")


def _digest(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def queue_taste_forget(*, hermes_home: str | Path, target: str, taste_id: int) -> None:
    """Stage tombstone evidence before Heart commits. Never lock the taste row."""
    from agent.secret_scope import build_profile_secret_scope
    from hermes_cli.heart.taste_context import normalized
    from hermes_constants import hermes_home_key
    from plugins.context_engine.lcm_x_store import PgConnection, agent_for_home

    home = Path(hermes_home).resolve()
    if hermes_home_key(home) == hermes_home_key():
        database_override = lcm_env_get("LCM_DATABASE_PATH")
    else:
        database_override = build_profile_secret_scope(home).get("LCM_DATABASE_PATH")
    database = Path(database_override) if database_override else home / "lcm.db"
    agent = agent_for_home(home)
    if str(database).startswith((":memory:", "file:")):
        raise ValueError("Taste forget requires a durable LCM database")
    routed = configured_store_backend(home) in {"auto", "postgres"} and not database.exists()
    label = normalized(target)
    if not label or taste_id <= 0:
        raise ValueError("Taste forget requires a label and a positive taste ID")
    key = f"{_KEY}{agent}:{taste_id}"
    evidence = f"{_digest(target)}:{len(label)}:{_digest(label)}"
    sql = "INSERT INTO metadata(key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING"
    if routed:
        with closing(PgConnection(str(database), agent)) as conn, conn:
            conn.execute(sql, (key, evidence))
    else:
        if store_backend(database) is not None:
            raise RuntimeError("The owning home's SQLite path already has a PostgreSQL route")
        with closing(SummaryDAG(database)) as dag, dag.connection as conn:
            conn.execute(sql, (key, evidence))


def taste_instructions(hermes_home: str | Path, instructions: str, *, forgetting: bool = False) -> str:
    """Append the exact policy only to an owning taste profile's summary requests."""
    if TASTE_INSTRUCTION in instructions.splitlines():
        return instructions
    if not forgetting and (not hermes_home or not _has_tastes(Path(hermes_home))):
        return instructions
    return "\n".join(part for part in (instructions, TASTE_INSTRUCTION) if part)


def _has_tastes(home: Path) -> bool:
    from hermes_state_store import StoreUnavailableError

    try:
        from psycopg.errors import UndefinedTable
    except ImportError:
        return False
    from hermes_cli.heart.taste_context import has_tastes

    try:
        return has_tastes(home)
    except (StoreUnavailableError, UndefinedTable):
        return False


def pending_targets(dag: SummaryDAG, home: Path) -> dict[int, set[str]]:
    """Return normalized hashes grouped by length for committed owner tombstones."""
    from hermes_cli.heart import rows
    from hermes_cli.heart.taste_context import normalized

    agent = rows.agent(home)
    prefix = f"{_KEY}{agent}:"
    queued = dag.connection.execute(
        "SELECT key, value FROM metadata WHERE key >= ? AND key < ?",
        (prefix, prefix[:-1] + ";"),
    ).fetchall()
    if not queued:
        return {}
    evidence = {int(key[len(prefix):]): value.split(":") for key, value in queued}
    with rows.open_store(home) as store, store._cursor() as cur:
        cur.execute("SELECT id, label_hash, label FROM memory.tastes WHERE agent_id=%s", (agent,))
        tastes = cur.fetchall()
    live = {_digest(normalized(label)) for _, _, label in tastes if label is not None}
    targets: dict[int, set[str]] = {}
    for taste_id, label_hash, label in tastes:
        if label is not None or taste_id not in evidence:
            continue
        original, length, digest = evidence[taste_id]
        if label_hash.hex() == original and digest not in live:
            targets.setdefault(int(length), set()).add(digest)
    return targets


def names_target(text: str, targets: dict[int, set[str]]) -> bool:
    from hermes_cli.heart.taste_context import normalized

    return next(target_spans(normalized(text), targets), None) is not None


def target_spans(text: str, targets: dict[int, set[str]]) -> Iterator[tuple[int, int]]:
    """Find term offsets in normalized text by testing only queued label lengths."""
    for start in _WORD_START.finditer(text):
        for length, digests in targets.items():
            end = start.start() + length
            if end <= len(text) and _WORD_END.match(text, end) and _digest(text[start.start():end]) in digests:
                yield start.start(), end
