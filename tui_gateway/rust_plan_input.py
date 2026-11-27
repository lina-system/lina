"""Prepare a Rust turn's input in the owning TUI session."""

from __future__ import annotations

import copy
import json
from collections.abc import Callable
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from tui_gateway.rust_plan_runtime import RustPlanState


def compatible_tool_pin(row: dict | None) -> bool:
    """Inspect the stored prefix before restore can narrow and rewrite a wider pin."""
    saved = (row or {}).get("tool_names")
    if not saved:
        return True
    try:
        pin = json.loads(saved)
    except (ValueError, TypeError):
        return False
    entries = pin.get("tools") if isinstance(pin, dict) else pin
    if not isinstance(entries, list):
        return False
    for entry in entries:
        if isinstance(entry, str):
            if entry != "read_file":
                return False
        elif not (isinstance(entry, dict) and entry.get("type") == "function"
                  and isinstance(fn := entry.get("function"), dict) and fn.get("name") == "read_file"):
            return False
    return True


def _pin_matches(state: RustPlanState, pin: dict | str, cfg: dict) -> bool:
    """A pin installed between turns must already be reflected in the cached runtime."""
    from hermes_cli.runtime_provider_custom import session_provider_identity

    if isinstance(pin, str):
        return pin == state.model
    for field in ("model", "base_url", "api_mode", "api_key"):
        if pin.get(field) and pin[field] != getattr(state, field):
            return False
    provider = pin.get("provider")
    return not provider or provider == state.provider or provider == session_provider_identity(
        state.provider, state.requested_provider, state.base_url, state.model, config=cfg)


def _same_transport_provider(state: RustPlanState, target: tuple[str, str], cfg: dict) -> str | None:
    """Recover the configured identity of the already-resolved custom chat route."""
    from hermes_cli.providers import custom_provider_slug
    from hermes_cli.runtime_provider_custom import _get_named_custom_provider, session_provider_identity

    model, provider = target
    if model != state.model or not provider:
        return None
    entry = _get_named_custom_provider(provider, config=cfg, resolve_credentials=False)
    if (not entry or entry["base_url"].rstrip("/") != state.base_url.rstrip("/")
            or entry.get("api_mode") not in (None, state.api_mode) or entry.get("extra_headers")
            or entry.get("key_cmd") or (entry.get("api_key") and entry["api_key"] != state.api_key)):
        return None
    canonical = custom_provider_slug(entry["name"], entry.get("provider_key", ""))
    active = session_provider_identity(state.provider, state.requested_provider, state.base_url, model, config=cfg)
    return canonical if active in (provider, canonical) else None


def _adopt_provider_identity(session: dict, state: RustPlanState, provider: str) -> None:
    from agent.agent_runtime_helpers import _apply_switched_provider_request_overrides, _persist_switch_billing_route
    from agent.native_compaction import resolve_native_compaction_capabilities
    from tui_gateway import server

    state.provider = state.requested_provider = provider
    state.runtime_capabilities = resolve_native_compaction_capabilities(
        model=state.model, provider=provider, base_url=state.base_url)
    _apply_switched_provider_request_overrides(state, state.provider)
    state.context_compressor.update_model(
        model=state.model, context_length=state.context_compressor.context_length,
        base_url=state.base_url, api_key=state.api_key, provider=state.provider, api_mode=state.api_mode,
    )
    state._cached_system_prompt = None
    _persist_switch_billing_route(state)
    server._persist_live_session_runtime(session, runtime=state)
    state.pin_tools_for_model_sync()
    server._persist_live_session_system_prompt(session, runtime=state)
    server._append_model_switch_marker(session, model=state.model, provider=state.provider)
    # The supervisor publishes this snapshot immediately after the child's message.start.
    display_session = {**session, "_compute_host_active": False}
    display_session.pop("_metadata_mirror", None)
    info = server._session_info(state, display_session)
    info["usage"] = server._get_usage(state)
    session["_rust_pre_model_info"] = info


def prepare_plan_input(
    sid: str, session: dict, state: RustPlanState, text: str, *, before_prepare: Callable[[], None] | None = None,
) -> tuple[RustPlanState, str, str, list[dict], int] | None:
    """Return the state, prompt, model input, detached history and version, or defer to Python.

    ``before_prepare`` snapshots readiness after eligibility and before a model marker or note.
    The caller owns the running slot and the state's profile DB handle.
    """
    from hermes_cli.fallback_config import get_fallback_chain
    from tui_gateway import server

    if (not isinstance(text, str) or "@" in text or session.get("attached_images")
            or session.get("pending_model_switch") or session.get("one_turn_model_restore")
            or session.get("voice_turn") or server._voice_mode_enabled() or server._voice_tts_enabled()):
        return None
    run = server._TurnRun(state, None, None, receipt_committed=False)
    try:
        server._bind_turn_input_scopes(sid, session, run.scopes)
        cfg = server._load_cfg() or {}
        if (not compatible_tool_pin(state._session_db.get_session(state.session_id))
                or get_fallback_chain(cfg) or server._is_bot_mode_session(session, runtime=state)
                or "setup_beat" in state.valid_tool_names):
            return None
        target = server._config_model_target()
        sync = server._config_model_sync_due(session, target)
        canonical = None
        if sync:
            model, provider = target
            if model != state.model or (provider and provider != state.provider):
                if session.get("slash_worker") is not None:
                    return None
                canonical = _same_transport_provider(state, target, cfg)
                if canonical is None:
                    return None
        elif not _pin_matches(state, session.get("model_override") or {}, cfg):
            return None

        if before_prepare is not None:
            before_prepare()
        cwd = session.get("cwd") or None
        if state.session_cwd != cwd:
            state.session_cwd = cwd
            if cwd:
                state._subdirectory_hints.rebind_working_dir(cwd)
        if sync:
            superseded_pin = server._record_config_model_target(session, target)
            if canonical is not None:
                _adopt_provider_identity(session, state, canonical)
            elif superseded_pin:
                server._persist_live_session_runtime(session, runtime=state)
        signature = server._tui_compression_config_signature(cfg)
        if signature != session.get("config_compression_seen"):
            server._apply_live_compression_config(state, cfg)
            session["config_compression_seen"] = signature
        server._adopt_out_of_band_turns(session)
        prepared = server._prepare_turn_message(sid, session, run, text, [])
        prompt, run_message, _, _ = prepared
        return state, prompt, run_message, copy.deepcopy(run.history), run.history_version
    finally:
        try:
            server._release_turn_scopes(sid, session, run)
        finally:
            if run.scopes.home is not None:
                server.reset_hermes_home_override(run.scopes.home)
