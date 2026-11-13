"""Honcho conclusions that have no vector of the configured embedding model yet (lina#644).

`lina migrate memory` copies an agent's conclusions without vectors and leaves them for
``lina store reembed --source memory``; until that run finishes, Honcho's searches only reach the embedded ones.
``honcho_search`` and recall say so, and ``lina doctor`` lists the counts per workspace. The count reads through the
store owner's connection, as ``reembed`` does: Honcho's ``ltm`` tables are not granted to ``lina_app``.
"""

from __future__ import annotations

import logging
import time
from typing import Any, Optional

logger = logging.getLogger(__name__)

# How long a count is reused: recall runs every turn, reembed takes minutes.
CHECK_EVERY_S = 300.0

# Live conclusions with text (the ones reembed embeds) and no vector of the model, per workspace.
_PENDING = """
    SELECT d.workspace_name, count(*) FROM ltm.documents d
    WHERE d.deleted_at IS NULL AND btrim(coalesce(d.content, '')) <> '' {workspace}
      AND NOT EXISTS (SELECT 1 FROM vec.embeddings e
                      WHERE e.kind = 'document' AND e.item_id = d.vec_id AND e.model = %(model)s)
    GROUP BY d.workspace_name ORDER BY d.workspace_name"""


def configured_model(config: Optional[dict[str, Any]] = None) -> str:
    """``embedding.model`` of config.yaml, the model reembed writes and searches read; the default when unset."""
    from agent.embedding_client import DEFAULT_MODEL
    if config is None:
        from hermes_cli.config import load_config
        config = load_config()
    return str((config.get("embedding") or {}).get("model") or DEFAULT_MODEL)


def pending_counts(model: str, workspace: Optional[str] = None) -> dict[str, int]:
    """Conclusions without a *model* vector per Honcho workspace (only *workspace* when given); empty when the
    store has no Honcho tables. Raises ``StoreUnavailableError`` when the store is not set up or not reachable."""
    from hermes_state_store import _owner_connection
    sql = _PENDING.format(workspace="AND d.workspace_name = %(workspace)s" if workspace else "")
    with _owner_connection() as conn:
        if conn.execute("SELECT to_regclass('ltm.documents') IS NULL").fetchone()[0]:
            return {}
        rows = conn.execute(sql, {"model": model, "workspace": workspace}).fetchall()
    return {str(name): int(count) for name, count in rows}


def partial_note(count: int) -> str:
    noun = "conclusion is" if count == 1 else "conclusions are"
    return (f"{count} {noun} not embedded yet; search is partial until `lina store reembed --source memory` "
            "finishes.")


class PendingNote:
    """The partial-search note for one workspace, counted at most every ``CHECK_EVERY_S`` seconds; "" when every
    conclusion has a vector or the count cannot be read (a Honcho outside the LINA store has nothing to count)."""

    def __init__(self, workspace: str, model_of=configured_model) -> None:
        self._workspace = workspace
        self._model_of = model_of
        self._note = ""
        self._checked_at: Optional[float] = None

    def note(self) -> str:
        now = time.monotonic()
        if self._checked_at is not None and now - self._checked_at < CHECK_EVERY_S:
            return self._note
        self._checked_at = now
        try:
            count = pending_counts(self._model_of(), self._workspace).get(self._workspace, 0)
        except Exception as exc:  # health: allow BLE001 -- a missing or down store must not cost recall or search
            logger.debug("Honcho pending-vector count unavailable: %s", exc)
            count = 0
        self._note = partial_note(count) if count else ""
        return self._note
