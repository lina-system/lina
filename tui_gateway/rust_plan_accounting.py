"""Session accounting and durable-lease hooks for the parent of a native turn."""

from __future__ import annotations

import os
import threading
import time

from agent.api_error_summary import ApiErrorSummaryMixin
from agent.status_output import StatusOutputMixin
from agent.turn_explainers import TurnExplainersMixin


class RustPlanAccounting(ApiErrorSummaryMixin):
    """The part of the turn/usage protocol exercised after native dispatch."""

    quiet_mode = True
    verbose_logging = False
    context_compressor = None
    _execution_thread_id = None  # No Python tool thread to signal.
    _turn_completion_explainer_enabled = TurnExplainersMixin._turn_completion_explainer_enabled
    _format_turn_completion_explanation = staticmethod(TurnExplainersMixin._format_turn_completion_explanation)
    _emit_diagnostic_status = StatusOutputMixin._emit_diagnostic_status
    _call_callback = StatusOutputMixin._call_callback

    def _emit_status(self, message: str) -> None:
        self._call_callback("status_callback", "lifecycle", message, origin="_emit_status")

    def _emit_warning(self, message: str) -> None:
        self._call_callback("status_callback", "warn", message, origin="_emit_warning")

    @property
    def _session_db(self):
        return self._borrowed_db

    @_session_db.setter
    def _session_db(self, db):
        # Planning and settlement each borrow a profile handle for their own scope.
        self._borrowed_db = db
        if self.context_compressor is not None:
            self.context_compressor._session_db = db

    def _liveness_activity_lock(self):
        return self._activity_lock

    def _touch_activity(self, description: str) -> None:
        with self._activity_lock:
            self._lease_turn_id = self._plan_session.get("_compute_host_turn_id")
            self._activity_start_ns = time.perf_counter_ns()
            self._activity_start_wall = time.time()
            self._last_activity_desc = description

    @property
    def _turn_liveness_activity_generation(self) -> int:
        session = self._plan_session
        stamp = session.get("_compute_host_activity_ns")
        if self._lease_turn_id == session.get("_compute_host_turn_id") and type(stamp) is int:
            if self._activity_start_ns < stamp <= time.perf_counter_ns():
                return stamp
        return self._activity_start_ns

    @property
    def _last_activity_ts(self) -> float:
        # The watchdog uses wall time. Convert the child's sample once relative
        # to turn start so repeated heartbeats compare equal at claim revalidation.
        delta_ns = self._turn_liveness_activity_generation - self._activity_start_ns
        return self._activity_start_wall + delta_ns / 1_000_000_000

    @property
    def _interrupt_requested(self) -> bool:
        # Admission polls this before lease.start(), including on a reused planner.
        session = self._plan_session
        return bool(session.get("_turn_cancel_requested") or session.get("_closing") or (
            self._interrupted_turn_id and self._interrupted_turn_id == session.get("_compute_host_turn_id")
        ))

    @_interrupt_requested.setter
    def _interrupt_requested(self, value: bool) -> None:
        # The durable lease also writes this field on interrupt/cleanup failure.
        self._interrupted_turn_id = self._plan_session.get("_compute_host_turn_id") if value else None

    def interrupt(self, message=None, *, hard_cancel=False, tool_reason=None, require_generation=None) -> bool:
        session = self._plan_session
        with session["history_lock"]:
            turn_id = session.get("_compute_host_turn_id")
            stage = session.get("_rust_turn_stage")
            if not turn_id or turn_id != self._lease_turn_id or not stage or stage.get("turn_id") != turn_id:
                return False
            if require_generation is not None and require_generation != self._turn_liveness_activity_generation:
                return False
            self._interrupt_message = message
            self._tool_interrupt_reason = tool_reason
            if hard_cancel:
                self._hard_interrupt_requested.set()
            session["_turn_cancel_requested"] = True
            host = session.get("_compute_host_turn_host")
        # submit_turn takes the host lock before history_lock. Its pre-dispatch
        # check sees the marker even when the host has not yet accepted a frame.
        if host is not None:
            host.interrupt(host.sid, turn_id=turn_id)
        return True

    def clear_interrupt(self) -> None:
        # The bridge owns the session cancellation bit and resets it at admission.
        self._interrupt_requested = False
        self._interrupt_message = self._tool_interrupt_reason = None
        self._hard_interrupt_requested.clear()


def _plan_compressor(state, cfg):
    from agent.agent_init import _parse_compression_config, config_context_length_for_runtime
    from agent.context_compressor import ContextCompressor
    from hermes_cli.config import get_custom_provider_context_length

    settings = _parse_compression_config(state, cfg)
    state.compression_enabled = settings.enabled
    state.max_compression_attempts = settings.max_attempts
    window = config_context_length_for_runtime(state, cfg) or get_custom_provider_context_length(
        state.model, state.base_url, config=cfg,
    )
    state._config_context_length = window
    # Without a configured window, build_plan_state uses the production context
    # engine to discover it with the resolved route.
    if window is None:
        return None
    return ContextCompressor(
        model=state.model, provider=state.provider, base_url=state.base_url,
        api_mode=getattr(state, "api_mode", "chat_completions"), quiet_mode=True,
        config_context_length=window, max_tokens=getattr(state, "max_tokens", None),
        threshold_percent=settings.threshold, model_thresholds=settings.model_thresholds,
        threshold_tokens_cap=settings.threshold_tokens, protect_first_n=settings.protect_first,
        protect_last_n=settings.protect_last, min_tail_user_messages=settings.min_tail_users,
        summary_target_ratio=settings.target_ratio, tail_mode=settings.tail_mode,
        abort_on_summary_failure=settings.abort_on_summary_failure,
        proactive_prune_tokens=settings.proactive_prune_tokens,
        proactive_prune_min_result_chars=settings.proactive_prune_min_chars,
        proactive_prune_min_reclaim_tokens=settings.proactive_prune_min_reclaim,
    )


def init_plan_accounting(state, *, cfg, session, db) -> None:
    """Initialize once per planner, after route selection, with a borrowed DB."""
    from agent.agent_init import _USAGE_STATE, _set_defaults
    from agent.empty_response_guard import resolve_guard_settings
    from agent.safe_worker_policy import safe_worker_enabled
    from agent.subdirectory_hints import SubdirectoryHintTracker
    from agent.usage_anchor import restore_usage_anchor
    from hermes_cli.config import cfg_get
    from utils import is_truthy_value

    _set_defaults(state, _USAGE_STATE)
    state._plan_session = session
    state._activity_lock = threading.Lock()
    state._lease_turn_id = None
    state._activity_start_ns = time.perf_counter_ns()
    state._activity_start_wall = time.time()
    state._session_persist_lock = threading.RLock()
    state._session_db_created = False
    state._active_session_turn_lease_holder = None
    state._active_session_turn_lease_ttl_seconds = None
    state._interrupted_turn_id = None
    state._interrupt_message = state._tool_interrupt_reason = None
    state._hard_interrupt_requested = threading.Event()
    state._empty_guard_enabled, state._empty_guard_cost_threshold_usd = resolve_guard_settings(
        cfg_get(cfg, "agent", "empty_response_guard"),
    )
    state._turn_completion_explainer_enabled_cache = bool(
        cfg_get(cfg, "display", "turn_completion_explainer", default=True)
    )
    if getattr(state, "_subdirectory_hints", None) is None:
        skip_rules = (getattr(state, "skip_context_files", False) or safe_worker_enabled()
                      or is_truthy_value(os.environ.get("HERMES_IGNORE_RULES")))
        state._subdirectory_hints = SubdirectoryHintTracker(working_dir=str(state.cwd), enabled=not skip_rules)
    state.context_compressor = _plan_compressor(state, cfg)
    state._session_db = db
    if state.context_compressor is not None:
        state.context_compressor.bind_session_state(db, state.session_id)
    restore_usage_anchor(state, session.get("history"))
