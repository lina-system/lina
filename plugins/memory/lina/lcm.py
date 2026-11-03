"""Hand a finished turn to the LCM-X engine of its session (lina#20).

LCM-X ingests each finished turn from its own ``post_llm_call`` hook. The hook stays registered for every agent;
for a session whose agent carries lina-memory, the context-engine loader (``plugins/context_engine``) asks this
module who takes the turn (``handover_for``) and lets lina-memory hand it over instead, so the policy table
applies and the turn is still one ingest. An agent without lina-memory (a ``skip_memory`` child, a background
review, the curator) claims no session, and LCM-X's hook ingests its turns as before. The engine of a session is
found through LCM-X's own ``engine_registry`` and used under its stable-use lease, and the turn is handed to it the
way the hook does: ``ingest`` (cursor-based, so history it already holds is skipped), then ``note_turn_complete``
and the after-turn leaf preparation (plugins/context_engine/lcm-x/__init__.py, ``_on_post_llm_call``)."""

from __future__ import annotations

import logging
import sys
import threading
import weakref
from typing import Any, Callable, Optional, Sequence

logger = logging.getLogger(__name__)

_REGISTRY_MODULE = "plugins.context_engine.lcm-x.engine_registry"


# session id -> the lina-memory handover of the agent that owns it (a weak method, so a dropped agent claims nothing).
_CLAIMS: dict[str, weakref.WeakMethod] = {}
_CLAIMS_LOCK = threading.Lock()


def claim_session(session_id: str, take_turn: Callable[..., None]) -> None:
    """lina-memory hands over this session's finished turns itself; LCM-X's hook leaves them to it."""
    if session_id:
        with _CLAIMS_LOCK:
            _CLAIMS[session_id] = weakref.WeakMethod(take_turn)


def release_session(session_id: str, take_turn: Callable[..., None]) -> None:
    """Drop the claim, if this handover still holds it (a later agent of the same session may have claimed it)."""
    with _CLAIMS_LOCK:
        claim = _CLAIMS.get(session_id)
        if claim is not None and claim() in (None, take_turn):
            del _CLAIMS[session_id]


def handover_for(session_id: str) -> Optional[Callable[..., None]]:
    """The lina-memory handover that takes this session's turn, or None when LCM-X's own hook should ingest it."""
    with _CLAIMS_LOCK:
        claim = _CLAIMS.get(session_id) if session_id else None
    return claim() if claim is not None else None


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
