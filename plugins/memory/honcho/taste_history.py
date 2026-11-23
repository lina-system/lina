"""Keep Honcho self-context out after the profile forgets its last live taste."""

from pathlib import Path

from hermes_cli.heart import rows


def has_taste_tombstones(home: Path) -> bool:
    with rows.open_store(home) as store, store._cursor() as cur:
        cur.execute(
            "SELECT EXISTS (SELECT 1 FROM memory.tastes WHERE agent_id=%s AND status='forgotten')",
            (store.agent_id,),
        )
        (has_tombstones,) = cur.fetchone()
        return has_tombstones
