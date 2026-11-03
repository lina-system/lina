"""Hand a finished turn to the LCM-X engine of its session (lina#20).

LCM-X used to ingest each turn from its own ``post_llm_call`` hook; with lina-memory in the memory slot the
context-engine loader does not register that hook (``plugins/context_engine``), and this is the one path. The
engine of a session is found through LCM-X's own ``engine_registry`` and used under its stable-use lease, and the
turn is handed to it the way the hook did: ``ingest`` (cursor-based, so history it already holds is skipped), then
``note_turn_complete`` and the after-turn leaf preparation (plugins/context_engine/lcm-x/__init__.py,
``_on_post_llm_call``)."""

from __future__ import annotations

import logging
import sys
from typing import Any, Optional, Sequence

logger = logging.getLogger(__name__)

_REGISTRY_MODULE = "plugins.context_engine.lcm-x.engine_registry"


def _registry():
    """LCM-X's engine registry when this process loaded the bundled engine, else None (LCM-X is not the
    context engine here, so there is nothing to hand the turn to)."""
    return sys.modules.get(_REGISTRY_MODULE)


def conversation_id(session_id: str) -> Optional[str]:
    """The LCM conversation the session's engine is bound to, read without taking the engine."""
    registry = _registry()
    engine = registry.resolve_active_lcm_engine(session_id=session_id) if registry and session_id else None
    return str(getattr(engine, "_conversation_id", "") or "") or None if engine is not None else None


def hand_turn(history: Sequence[dict[str, Any]], *, session_id: str) -> bool:
    """Ingest ``history`` (the agent's transcript through this turn) into the session's LCM engine. True when an
    engine took it."""
    registry = _registry()
    if registry is None or not session_id or not history:
        return False
    messages = list(history)

    def ingest(engine) -> None:
        try:
            engine.ingest(messages)
        finally:
            engine.note_turn_complete()
        prepare = getattr(engine, "_prepare_leaves_after_turn", None)
        if callable(prepare):
            prepare(messages)

    result = registry.use_active_lcm_engine(ingest, session_id=session_id, timeout=None)
    if not result.used:
        logger.warning("lina-memory: no LCM engine took the turn of session %s (%s)", session_id, result.status.value)
    return result.used
