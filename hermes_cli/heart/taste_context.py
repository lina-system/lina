"""Read the owning profile's tastes for the frozen section and the current turn."""

from __future__ import annotations

import re
import unicodedata
from pathlib import Path

from hermes_cli.heart import rows


# Korean attaches particles to a noun instead of separating them with a space.
_TARGET_END = r"(?:에서|에게|으로|까지|부터|처럼|은|는|이|가|을|를|의|에|로|와|과|도|만)?(?!\w)"


def has_tastes(home: Path) -> bool:
    with rows.open_store(home) as store, store._cursor() as cur:
        cur.execute("SELECT EXISTS (SELECT 1 FROM memory.tastes WHERE agent_id=%s AND live)", (store.agent_id,))
        return cur.fetchone()[0]


def _prompt_label(label: str) -> bool:
    # Imported ledgers predate the refiner's short target check. One record
    # must not add another prompt line or an opening/closing section marker.
    return 0 < len(label) <= 80 and label.isprintable() and "<" not in label and ">" not in label


def core_lines(home: Path) -> tuple[str, ...]:
    with rows.open_store(home) as store, store._cursor() as cur:
        # Daily scoring may change even a core row's valence. Freeze the
        # direction from its promotion, in immutable id order, not score order.
        cur.execute("""
            SELECT t.label, coalesce((
                SELECT e.to_valence FROM memory.taste_events e
                WHERE e.agent_id=t.agent_id AND e.taste_id=t.id AND e.to_status='core'
                    AND e.from_status IS DISTINCT FROM 'core' AND e.to_valence IS NOT NULL
                ORDER BY e.changed_at DESC, e.event_id DESC LIMIT 1
            ), (
                SELECT e.from_valence FROM memory.taste_events e
                WHERE e.agent_id=t.agent_id AND e.taste_id=t.id AND e.from_status='core'
                    AND e.from_valence IS NOT NULL
                ORDER BY e.changed_at, e.event_id LIMIT 1
            ), t.valence)
            FROM memory.tastes t WHERE t.agent_id=%s AND t.status='core' AND t.protected_passed IS NOT FALSE
            ORDER BY t.id
        """, (store.agent_id,))
        return tuple(f"{direction}: {label}" for label, direction in cur if _prompt_label(label))[:5]


def pick_lines(home: Path, query: str) -> tuple[str, ...]:
    query = unicodedata.normalize("NFKC", query).casefold()
    if not query.strip():
        return ()
    with rows.open_store(home) as store, store._cursor() as cur:
        cur.execute("""
            SELECT label, valence FROM memory.tastes
            WHERE agent_id=%s AND status='active' AND protected_passed IS NOT FALSE
            ORDER BY strength DESC, id
        """, (store.agent_id,))
        picked = []
        for label, direction in cur:
            if not _prompt_label(label):
                continue
            target = unicodedata.normalize("NFKC", label).casefold()
            if re.search(r"(?<!\w)" + re.escape(target) + _TARGET_END, query):
                picked.append(f"{direction}: {label}")
                if len(picked) == 3:
                    break
        return tuple(picked)
