"""The model, provider and reasoning effort a kanban card session runs on (lina#346).

A goal or work-card session runs on the deployment's card-session model, ``kanban.worker_model`` /
``kanban.worker_provider`` / ``kanban.worker_reasoning_effort`` (the key names Hermes uses, so one
config.yaml serves both), unless the card names its own: the card's ``model_override`` (with its
``provider_override``) and ``reasoning_effort`` win, each on its own. A review run keeps the agent's
own model, as conversations do; they never come through here.

Each key is read from the agent profile's config.yaml when it sets it, else from the deployment
root's, else ``DEFAULT_CONFIG``. Model and provider are one unit: the provider comes from the config
that chose the model, so a profile's model is never paired with another config's provider. An empty
value means the agent's own model (or effort).
"""
from __future__ import annotations

import logging
from typing import Any, Optional

logger = logging.getLogger(__name__)


def _deployment_kanban() -> dict:
    """The deployment root's ``kanban`` section (``{}`` when unreadable)."""
    from hermes_cli.config_effective import load_user_config_effective
    from hermes_constants import get_default_hermes_root

    try:
        return dict((load_user_config_effective(get_default_hermes_root() / "config.yaml") or {}).get("kanban") or {})
    except (OSError, ValueError, TypeError):
        logger.warning("kanban session model: deployment config unreadable", exc_info=True)
        return {}


def _sources(config: dict) -> list[dict]:
    from hermes_cli.config_defaults import DEFAULT_CONFIG

    return [dict(config.get("kanban") or {}), _deployment_kanban(), DEFAULT_CONFIG["kanban"]]


def _text(value: Any) -> str:
    return value.strip() if isinstance(value, str) else ""


def _default_route(sources: list[dict]) -> tuple[str, str]:
    """``(model, provider)`` from the first config that sets ``worker_model`` at all."""
    for kanban in sources:
        if "worker_model" in kanban:
            model = _text(kanban.get("worker_model"))
            return model, _text(kanban.get("worker_provider")) if model else ""
    return "", ""


def _default_effort(sources: list[dict]) -> str:
    from hermes_cli.kanban_db import normalize_reasoning_effort

    for kanban in sources:
        if "worker_reasoning_effort" not in kanban:
            continue
        try:
            return normalize_reasoning_effort(_text(kanban.get("worker_reasoning_effort"))) or ""
        except ValueError as exc:
            # An invalid value must not fail every card spawn: skip it and read the next config.
            logger.warning("kanban: ignoring kanban.worker_reasoning_effort (%s)", exc)
    return ""


def card_session_route(task, config: dict, *, review: bool) -> tuple[dict[str, str], dict[str, str]]:
    """``(params, origin)``: the ``model`` / ``provider`` / ``reasoning`` session params for *task*
    and where the model and the effort came from (``card`` / ``deployment`` / ``agent``)."""
    sources = [] if review else _sources(config)
    params: dict[str, str] = {}
    origin = {"model": "agent", "reasoning": "agent"}
    if task.model_override:
        params["model"], origin["model"] = task.model_override, "card"
        if task.provider_override:
            params["provider"] = task.provider_override
    elif sources:
        model, provider = _default_route(sources)
        if model:
            params["model"], origin["model"] = model, "deployment"
        if provider:
            params["provider"] = provider
    if task.reasoning_effort:
        params["reasoning"], origin["reasoning"] = task.reasoning_effort, "card"
    elif sources:
        effort = _default_effort(sources)
        if effort:
            params["reasoning"], origin["reasoning"] = effort, "deployment"
    return params, origin


def goal_context_bound() -> int:
    """``kanban.goal_context_bound_tokens`` of the bound profile: the context size at which a goal-mode run
    hands off to a new session (0 = never; an unusable value falls back to the default)."""
    from hermes_cli.config import load_config
    from hermes_cli.config_defaults import DEFAULT_CONFIG

    default = DEFAULT_CONFIG["kanban"]["goal_context_bound_tokens"]
    raw = ((load_config() or {}).get("kanban") or {}).get("goal_context_bound_tokens", default)
    try:
        return max(0, int(raw))
    except (TypeError, ValueError):
        logger.warning("kanban: invalid kanban.goal_context_bound_tokens (%s); using %d", type(raw).__name__, default)
        return default


def session_model_record(policy, origin: dict[str, str]) -> dict[str, Optional[str]]:
    """The card's ``session_model`` event payload: the model and effort the session runs on."""
    reasoning = policy.reasoning_config
    effort = None if not reasoning else (reasoning.get("effort") if reasoning.get("enabled") else "none")
    return {"model": policy.model, "provider": policy.provider, "reasoning": effort,
            "model_from": origin["model"], "reasoning_from": origin["reasoning"]}
