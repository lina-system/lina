"""Prompt and tool state for a session whose turn is planned on the Rust path."""

from __future__ import annotations

import os
from collections.abc import Iterator
from contextlib import contextmanager
from copy import deepcopy
from typing import TYPE_CHECKING, Any

from agent import agent_runtime_helpers as runtime
from agent import system_prompt as prompts
from agent.reasoning_params import ReasoningParamsMixin


class _PlanningPromptDB:
    """Forward prompt operations to the borrowed DB and stage tool pins for admission."""

    def __init__(self, db: Any, state: RustPlanPrompt) -> None:
        self.db = db
        self.state = state

    def __getattr__(self, name: str) -> Any:
        return getattr(self.db, name)

    def update_session_tool_names(self, session_id: str, pin: dict[str, Any]) -> None:
        if session_id != self.state.session_id:
            raise ValueError("Tool pin does not belong to the planned session")
        self.state.pending_tool_pin = deepcopy(pin)


class RustPlanPrompt:
    """Production methods used by prompt restore, transcript repair and request assembly."""

    _build_system_prompt = prompts.build_system_prompt
    _invalidate_system_prompt = prompts.invalidate_system_prompt
    _repair_message_sequence = runtime.repair_message_sequence
    _sanitize_api_messages = staticmethod(runtime.sanitize_api_messages)
    _drop_thinking_only_and_merge_users = staticmethod(runtime.drop_thinking_only_and_merge_users)
    _copy_reasoning_content_for_api = runtime.copy_reasoning_content_for_api
    _reasoning_replay_route = ReasoningParamsMixin._reasoning_replay_route
    _reasoning_echo_opt_in = ReasoningParamsMixin._reasoning_echo_opt_in
    _should_sanitize_tool_calls = ReasoningParamsMixin._should_sanitize_tool_calls
    _sanitize_tool_calls_for_strict_api = staticmethod(ReasoningParamsMixin._sanitize_tool_calls_for_strict_api)
    _anthropic_prompt_cache_policy = runtime.anthropic_prompt_cache_policy
    _direct_native_anthropic_tool_cache_capability = runtime._direct_native_anthropic_tool_cache_capability

    if TYPE_CHECKING:
        # RustPlanAccounting owns the borrowed handle and its compressor binding.
        from tui_gateway.rust_plan_accounting import RustPlanAccounting

        _session_db = RustPlanAccounting._session_db

    session_id: str = ""
    _cached_system_prompt: str | None = None
    _prompt_refresh_epoch: int | None = None
    pending_tool_pin: dict[str, Any] | None = None

    @contextmanager
    def _stage_tool_pins(self) -> Iterator[None]:
        db = self._session_db
        self._session_db = _PlanningPromptDB(db, self)
        try:
            yield
        finally:
            self._session_db = db

    def pin_tools_for_model_sync(self) -> None:
        """Restore or seed the tool pin before the prologue caches the switched prompt."""
        from agent.conversation_loop import _restore_pinned_tools
        from agent.prompt_refresh import _applied_epoch

        row = self._session_db.get_session(self.session_id)
        with self._stage_tool_pins():
            # A draft with no durable row yet needs the same first pin as a
            # session with an empty pin. Its row exists by lease admission.
            _restore_pinned_tools(self, row if row is not None else {})
        # The switch fills the prompt cache outside the normal restore/build path.
        # A refresh newer than the persisted epoch still belongs to the turn prologue.
        self._prompt_refresh_epoch = _applied_epoch(row)

    def refresh_plan_prompt(self, history: list[dict[str, Any]]) -> None:
        """Prepare the session's prompt and complete tool array for this turn's admission check."""
        from agent.conversation_loop import _restore_or_build_system_prompt
        from agent.delegation_backends import apply_requested_tool_rebuild
        from agent.prompt_refresh import refresh_warm_prompt
        from agent.turn_context import _refresh_mcp_tools_between_turns
        from tools.bot_mode_dm import ensure_message_agent_tool

        with self._stage_tool_pins():
            # An MCP change stages a replacement pin. On a cold resume that
            # append-only refresh must start from the persisted prefix.
            cold_resume = bool(history) and self._cached_system_prompt is None
            if not cold_resume:
                _refresh_mcp_tools_between_turns(self)
            refresh_warm_prompt(self, None, history)
            if self._cached_system_prompt is None:
                _restore_or_build_system_prompt(self, None, history)
            if cold_resume:
                _refresh_mcp_tools_between_turns(self)
            apply_requested_tool_rebuild(self)
            ensure_message_agent_tool(self)


def init_plan_prompt(
    state: Any, *, cfg: dict[str, Any], session: dict[str, Any], enabled_toolsets: list[str] | None,
    disabled_toolsets: list[str] | None, system_prompt: str | None, ignore_rules: bool,
) -> bool:
    """Initialize a fresh state's full tool snapshot under the owning session's profile.

    The caller supplies resolved model/provider/base_url/api_mode, platform, session_id
    and the borrowed _session_db. False declines the Rust path. The caller checks the
    resulting tools and hooks, then uses the normal turn-time restore/build path with history.
    That path owns prompt and tool persistence and can widen the surface on restore.
    """
    from agent.agent_init import _cfg_dict, _init_memory, _init_prompt_cache_config, _load_tools
    from agent.memory_provider import is_core_memory_provider
    from agent.safe_worker_policy import safe_worker_enabled
    from agent.shell_hooks import register_from_config
    from hermes_cli import mcp_startup
    from plugins.context_engine import release_unselected_engines, selected_engine_name
    from tools.bot_mode_dm import ensure_message_agent_tool
    from tools.memory_tool import get_builtin_memory_config
    from tui_gateway import entry, server
    from utils import is_truthy_value

    safe_worker = safe_worker_enabled()
    skip_rules = ignore_rules or safe_worker
    if state.api_mode != "chat_completions" or state.provider == "moa" or selected_engine_name(cfg) is not None:
        return False
    if not skip_rules and not is_core_memory_provider(get_builtin_memory_config(cfg).get("provider")):
        return False
    # bind_module publishes these TUI helpers against the live server namespace.
    if not safe_worker and server._load_prefill_messages():
        return False

    # A deferred agent gets a late refresh when a slow MCP server registers. A
    # planner cannot freeze a partial snapshot in the session's tools[] pin.
    mcp_startup.wait_for_mcp_discovery()
    entry.wait_for_mcp_discovery()
    if entry.mcp_discovery_in_flight():
        return False
    register_from_config(cfg)
    state.quiet_mode = True
    state.enabled_toolsets = enabled_toolsets
    state.prefill_messages = []
    state._reasoning_echo_flag = bool(_cfg_dict(cfg, "model").get("reasoning_echo"))
    state._reasoning_rejecting_routes = {}
    state._image_rejecting_models = set()
    _init_prompt_cache_config(state)
    _load_tools(state, enabled_toolsets, disabled_toolsets)
    _init_memory(state, cfg, skip_rules, state.platform)
    if not safe_worker:
        release_unselected_engines(cfg)

    state._context_engine_tool_names = set()  # The built-in compressor contributes no schemas.
    state._cached_system_prompt = None
    state._cached_system_prompt_static = None
    state.ephemeral_system_prompt = system_prompt or None
    state.pass_session_id = is_truthy_value(os.environ.get("HERMES_TUI_PASS_SESSION_ID"))
    state.load_soul_identity = False
    state.skip_context_files = skip_rules
    state._context_cwd_is_launch_artifact = server._context_cwd_is_launch_artifact(session)
    state.session_cwd = server._session_cwd(session) or None
    state._session_title_hint = str(session.get("pending_title") or "").strip() or None
    state._platform_hint_overrides = _cfg_dict(cfg, "platform_hints")
    agent_cfg = _cfg_dict(cfg, "agent")
    state._tool_use_enforcement = agent_cfg.get("tool_use_enforcement", "auto")
    state._execution_guidance = agent_cfg.get("execution_guidance", "auto")
    for key in ("task_completion_guidance", "parallel_tool_call_guidance", "environment_probe", "bot_mode_protocol"):
        setattr(state, f"_{key}", bool(agent_cfg.get(key, True)))
    # The normal turn prologue can add this authorized tool even on a fresh, unpinned session.
    ensure_message_agent_tool(state)
    return True
