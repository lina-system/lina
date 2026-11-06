"""Hand a finished turn to the LCM-X engine of its session (lina#20).

LCM-X ingests each finished turn from its own ``post_llm_call`` hook. The hook stays registered for every agent;
lina-memory claims its own agent's session through the context-engine loader's generic turn handover
(``plugins.context_engine.claim_turn_handover``), and the hook then leaves that session's turns to lina-memory, so
the policy table applies and the turn is still one ingest. An agent without lina-memory (a ``skip_memory`` child, a
background review, the curator) claims no session, and LCM-X's hook ingests its turns as before. The engine of a
session is found through LCM-X's own ``engine_registry`` and used under its stable-use lease, and the turn is handed
to it the way the hook does: ``ingest`` (cursor-based, so history it already holds is skipped), then
``note_turn_complete`` and the after-turn leaf preparation (plugins/context_engine/lcm-x/__init__.py,
``_on_post_llm_call``)."""

from __future__ import annotations

import logging
import sys
from typing import Any, Callable, Optional, Sequence

logger = logging.getLogger(__name__)

_REGISTRY_MODULE = "plugins.context_engine.lcm-x.engine_registry"


def claim_session(session_id: str, take_turn: Callable[..., None]) -> None:
    """lina-memory hands over this session's finished turns itself; LCM-X's hook leaves them to it."""
    from plugins.context_engine import claim_turn_handover
    claim_turn_handover(session_id, take_turn)


def release_session(session_id: str, take_turn: Callable[..., None]) -> None:
    """Drop the claim, if this handover still holds it (a later agent of the same session may have claimed it)."""
    from plugins.context_engine import release_turn_handover
    release_turn_handover(session_id, take_turn)


def _registry():
    """LCM-X's engine registry when this process loaded the bundled engine, else None (LCM-X is not the
    context engine here, so there is nothing to hand the turn to)."""
    return sys.modules.get(_REGISTRY_MODULE)


def conversation_id(session_id: str) -> Optional[str]:
    """The LCM conversation the session's engine is bound to, read without taking the engine."""
    registry = _registry()
    engine = registry.resolve_active_lcm_engine(session_id=session_id) if registry and session_id else None
    return str(getattr(engine, "_conversation_id", "") or "") or None if engine is not None else None


def use_engine(session_id: str, operation: Callable[[Any], Any], *, timeout: float = 5.0) -> tuple[bool, Any, str]:
    """Run ``operation(engine)`` with the session's LCM engine under its stable-use lease: ``(used, value, why
    not)``. Not used when LCM-X is not the context engine here or holds no engine for the session."""
    registry = _registry()
    if registry is None:
        return False, None, "LCM-X is not the context engine of this agent"
    if not session_id:
        return False, None, "no session is open"
    result = registry.use_active_lcm_engine(operation, session_id=session_id, timeout=timeout)
    return result.used, result.value, "" if result.used else f"no LCM-X engine for this session ({result.status.value})"


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
