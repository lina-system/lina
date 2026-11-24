"""Adapt the production agent to the deliberately narrow Rust turn protocol.

The bridge admits ownership before preparation, holds ``rust_turn_lease`` from
dispatch through persistence, and buffers message.complete until persistence
succeeds. Both entry points run outside history_lock. Per-read policy/redaction
must run in Python under the original session's profile before model egress.
The returned environment is a COMPLETE model environment, never an overlay.
"""

from __future__ import annotations

import contextlib
import copy
import ipaddress
import json
import re
import threading
import time
from collections.abc import Iterator
from pathlib import Path
from typing import TYPE_CHECKING, Literal
from urllib.parse import urlsplit

if TYPE_CHECKING:
    from tui_gateway.contracts.compute_host import ContextPreflight, ContextResponseUsage, JsonObject


_BODY_PARAMS = frozenset({
    "frequency_penalty", "logit_bias", "logprobs", "max_completion_tokens",
    "max_tokens", "parallel_tool_calls", "presence_penalty", "reasoning_effort",
    "response_format", "seed", "service_tier", "stop", "stream_options",
    "temperature", "tool_choice", "top_logprobs", "top_p", "timeout",
    "prompt_cache_key", "prompt_cache_retention",
})
ConversationHeader = Literal[
    "session-id", "x-opencode-session", "x-litellm-session-id", "x-grok-conv-id",
]
_AFFINITY_HEADERS: tuple[ConversationHeader, ...] = (
    "session-id", "x-opencode-session", "x-litellm-session-id", "x-grok-conv-id",
)


class RustTurnConflict(RuntimeError):
    """The prepared turn no longer owns the transcript it was built against."""


def _agent_for_turn(sid: str, session: dict, frame: dict):
    from tui_gateway import server

    # Ordinary Rust settlement does not drive the owning profile's goal judge
    # and continuation dispatcher.
    if session.get("session_key") and server._active_goal_manager(session) is not None:
        return None
    if session.get("agent") is None:
        server._start_agent_build(sid, session)
        if server._wait_agent_for_prompt(session, str(frame.get("request_id") or ""), sid):
            return None
        # The isolated Python creation route initializes the session directly,
        # without the parent lazy builder's config-sync baseline. Rust adoption
        # must run that same first-turn sync before freezing its prompt/history.
        session.pop("config_model_seen", None)
    return session.get("agent")


def _compatible_agent(agent) -> bool:
    """Require the actual OpenAI transport and the complete read-only tool set."""
    from openai import OpenAI
    from agent.context_compressor import ContextCompressor
    from hermes_cli.plugins import has_hook, has_middleware
    from hermes_cli.plugins_dispatch import stream_held
    from run_agent import AIAgent

    return (
        isinstance(agent, AIAgent)
        and isinstance(agent.client, OpenAI)
        and agent.api_mode == "chat_completions"
        and bool(agent.tools)
        and all(t.get("type") == "function" and t.get("function", {}).get("name") == "read_file"
                for t in agent.tools)
        and not getattr(agent, "_persist_disabled", False)
        and not getattr(agent, "credential_pool", None)
        and not getattr(agent, "prefill_messages", None)
        and not getattr(agent, "_memory_manager", None)
        and type(agent.context_compressor) is ContextCompressor
        # Python captures held transforms before streaming and rewrites before
        # persistence. The Rust host can publish deltas and commit raw output.
        and not stream_held()
        and not has_hook("transform_llm_output")
        and not any(has_hook(name) for name in ("pre_llm_call", "pre_api_request", "pre_tool_call"))
        and not any(has_middleware(name) for name in (
            "llm_request", "llm_execution", "tool_request", "tool_execution"))
    )


def _model_environment(agent) -> dict[str, str] | None:
    """Export only the hydrated primary runtime credential, with no URL auth."""
    url = urlsplit(str(agent.base_url or ""))
    if not url.hostname or url.username or url.password or url.query or url.fragment:
        return None
    try:
        loopback = ipaddress.ip_address(url.hostname).is_loopback
    except ValueError:
        loopback = False
    if url.scheme != "https" and not (url.scheme == "http" and loopback):
        return None
    client_kwargs = agent._client_kwargs
    # A bearer-only Rust client cannot preserve custom headers/query/TLS/auth.
    if set(client_kwargs) - {"api_key", "base_url", "timeout"}:
        return None
    key = client_kwargs.get("api_key")
    if not isinstance(key, str) or not key or "\r" in key or "\n" in key:
        return None
    if key != agent.api_key or str(client_kwargs.get("base_url")) != str(agent.base_url):
        return None
    return {"OPENAI_API_KEY": key}


def _prepare_input(sid: str, session: dict, text: str):
    """Run the production TUI prologue and release its thread-local scopes."""
    from tui_gateway import server

    state = server._TurnRun(
        agent=session["agent"], one_turn_restore=None, terminal_callback=None,
        receipt_committed=False)
    try:
        prepared = server._prepare_turn_input(sid, session, state, text, [])
        if prepared is None:
            return None
        prompt, run_message, _, _ = prepared
        return (
            state.agent, prompt, run_message,
            copy.deepcopy(state.history), state.history_version,
        )
    finally:
        server._release_turn_scopes(sid, session, state)
        if state.scopes.home is not None:
            server.reset_hermes_home_override(state.scopes.home)


def prepare_rust_turn(sid: str, session: dict, frame: dict) -> tuple[dict, dict[str, str]] | None:
    """Return the real assembled request and minimal auth, or refuse this route.

    Caller owns the admitted running slot and supplies a server-built turn.start,
    never raw client route fields. No model call is made here. ``frame`` is
    rewritten only after eligibility succeeds: its history/text become the real
    API projection while the original durable user row stays staged in Python.
    The bridge must install the Python per-read policy callback before dispatch.
    """
    from hermes_platform.host.facts import os_family

    if os_family() != "linux" or not Path("/proc/self/fd").is_dir():
        return None

    from agent.chat_completion_helpers import build_api_kwargs
    from agent.conversation_loop import _restore_or_build_system_prompt
    from agent.opencode_affinity import resolve_affinity_key
    from agent.prompt_refresh import refresh_warm_prompt
    from agent.turn_api_call import _should_stream
    from agent.turn_context import (
        _merge_gateway_notes, compose_user_api_content, stage_date_note, stage_skills_index_note,
    )
    from agent.turn_request_assembly import assemble_api_request
    from agent.gemini_native_adapter import is_native_gemini_base_url
    from tools.environments.local import LocalEnvironment
    from tools.file_tools import _get_file_ops
    from tui_gateway import server

    if (not session.get("running") or session.get("_closing")
            or session.get("_turn_cancel_requested") or session.get("pending_model_switch")
            or frame.get("attached_images") or session.get("attached_images")
            or not isinstance(frame.get("text"), str) or "@" in frame["text"]
            or (session.get("seeded") and not session.get("_branch_seed_persisted"))):
        return None
    with server._session_profile_runtime_scope(session):
        agent = _agent_for_turn(sid, session, frame)
        if agent is None or not _compatible_agent(agent):
            return None
        with session["history_lock"]:
            if frame.get("history_version", 0) != int(session.get("history_version", 0)):
                return None
        # Like ComputeHost._run_real_turn, create the owning row before the
        # prologue: a model-sync marker is already a durable transcript write.
        if not server._ensure_session_db_row(session):
            return None
        prepared = _prepare_input(sid, session, frame["text"])
        if prepared is None:
            return None
        agent, prompt, run_message, history, version = prepared
        if not _compatible_agent(agent):
            return None
        env = _model_environment(agent)
        if env is None:
            return None
        key = str(session.get("session_key") or "")
        if not key or str(agent.session_id) != key or frame.get("session_key") != key:
            return None
        if (agent._session_db is None
                or Path(agent._session_db.db_path).resolve().parent != server._session_home(session).resolve()):
            return None
        # This resolves the actual per-task backend, including overrides and
        # refusal scopes. A host-path-capable remote backend is still ineligible.
        tokens = server._set_session_context(key, cwd=server._session_cwd(session))
        try:
            file_ops = _get_file_ops(key)
            if type(file_ops.env) is not LocalEnvironment:
                return None
            refresh_warm_prompt(agent, None, history)
            if agent._cached_system_prompt is None:
                _restore_or_build_system_prompt(agent, None, history)
            # A persisted tool pin can restore a wider surface.
            if not _compatible_agent(agent):
                return None
            agent._current_turn_timestamp = time.time()
            user = {"role": "user", "content": run_message}
            messages = [*history, user]
            stage_skills_index_note(agent, agent._cached_system_prompt, messages)
            stage_date_note(agent, messages)
            plugin_context = _merge_gateway_notes(agent, messages, len(history), "")
            user_api_content = compose_user_api_content(run_message, "", plugin_context)
            if user_api_content is None and run_message != prompt:
                user_api_content = run_message
            assembled = assemble_api_request(
                agent, messages=messages, current_turn_user_idx=len(history),
                _ext_prefetch_cache="", _plugin_user_context=plugin_context,
                moa_config=None, active_system_prompt=agent._cached_system_prompt,
                original_user_message=frame["text"], pending_moa_prepared_request=None,
                request_logger=server.logger)
            if agent.context_compressor.should_compress(assembled.request_pressure_tokens):
                return None
            kwargs = build_api_kwargs(agent, assembled.api_messages, assembled.tools_for_api)
            affinity_key = resolve_affinity_key(agent.session_id)
        finally:
            server._clear_session_context(tokens)
        wire = kwargs.pop("messages")
        model = kwargs.pop("model")
        tools = kwargs.pop("tools", [])
        kwargs.pop("stream", None)
        stream = _should_stream(agent)
        if (stream and not is_native_gemini_base_url(agent.base_url)
                and not getattr(agent, "_stream_options_unsupported", False)):
            kwargs["stream_options"] = {"include_usage": True}
        headers = kwargs.pop("extra_headers", {})
        session_affinity: dict[ConversationHeader, str] = {
            name: affinity_key for name in _AFFINITY_HEADERS if name in headers
        }
        if (headers != session_affinity or (headers and not affinity_key)
                or "\r" in affinity_key or "\n" in affinity_key
                or (headers and (len(affinity_key) > 128 or not affinity_key.isascii()
                                or not all(c.isalnum() or c in "._:-" for c in affinity_key)))
                or set(kwargs) - _BODY_PARAMS):
            return None
        if session_affinity:
            kwargs["session_affinity"] = session_affinity
        if (not wire or wire[0].get("role") != "system"
                or not isinstance(wire[0].get("content"), str)
                or wire[-1].get("role") != "user"
                or not isinstance(wire[-1].get("content"), str)):
            return None
        plan = {
            "system": wire[0]["content"], "tools": tools, "model": model,
            "base_url": str(agent.base_url), "params": kwargs, "stream": bool(stream),
            "max_iterations": agent.max_iterations,
        }
        # Reject non-JSON provider extensions before anything crosses the pipe.
        try:
            encoded = json.dumps(plan, allow_nan=False)
        except (TypeError, ValueError):
            return None
        if env["OPENAI_API_KEY"] in encoded:
            return None
        with session["history_lock"]:
            if (session.get("history_version", 0) != version
                    or session.get("session_key") != key or session.get("_closing")):
                return None
            session["_rust_turn_stage"] = {
                "session_key": key, "history_version": version, "history": history,
                "compression_count": getattr(agent.context_compressor, "compression_count", None),
                "text": prompt, "api_text": wire[-1]["content"],
                "user_api_content": user_api_content,
                "request_id": frame.get("request_id"), "turn_id": frame.get("turn_id"),
                "display_kind": frame.get("display_kind"),
                "display_metadata": copy.deepcopy(frame.get("display_metadata")),
                "context_system": plan["system"],
                "wire_history": copy.deepcopy(wire[1:-1]),
                "session_affinity": session_affinity,
            }
            frame["history"] = wire[1:-1]
            frame["history_version"] = version
            frame["text"] = wire[-1]["content"]
            frame["cwd"] = str(server._session_cwd(session))
            frame["terminal_backend"] = "local"
        return plan, env


@contextlib.contextmanager
def rust_turn_lease(session: dict, frame: dict) -> Iterator[None]:
    """Hold the production durable lease after eligibility, through final commit.

    Enter/exit may happen on different threads. All profile tokens are reset
    BEFORE yielding; the lease's refresh threads carry their own context.
    Parent retains this manager and exits it exactly once on success, dispatch
    failure, cancellation, or process death, before normal completion handling.
    """
    from agent.turn_facade_lease import admit_durable_turn_lease
    from agent.conversation_loop import _persist_system_prompt
    from tui_gateway import server

    agent = session["agent"]
    with server._session_profile_runtime_scope(session, hydrate_secrets=False):
        if not server._ensure_session_db_row(session):
            raise RustTurnConflict("session store unavailable")
        with server._session_db(session) as db:
            if db is None or db.get_session(session["session_key"]) is None:
                raise RustTurnConflict("Rust turns require a durable session store")
            server._reopen_if_finalized(db, session["session_key"])
        admission = admit_durable_turn_lease(
            agent, session_id=session["session_key"],
            relay_turn_id=str(frame.get("turn_id") or frame["request_id"]),
            task_context={"platform": agent.platform, "session_id": session["session_key"]},
            conversation_history=copy.deepcopy(session.get("history", [])))
    lease = admission.lease
    if lease is None:
        raise RustTurnConflict("durable turn lease unavailable")
    try:
        stage = session.get("_rust_turn_stage")
        if (not stage or admission.conversation_history != stage["history"]
                or agent.session_id != stage["session_key"]):
            raise RustTurnConflict("transcript changed during lease admission")
        with server._session_profile_runtime_scope(session, hydrate_secrets=False):
            # A first prompt can be built while the draft has no row. Publish
            # its frozen prompt/tool pin only after durable ownership.
            _persist_system_prompt(
                agent, "Rust turn system prompt persist failed (session=%s): %s",
                persist_tools=True)
            lease.start()
        with session["history_lock"]:
            user = _accepted_rust_user(session, stage)
            _append_rust_rows(session, stage, [user])
            stage["rows"] = [user]
            # A send failure can fall back to the Python runner. Its existing
            # adoption path must reuse this committed send rather than append it.
            session["_submit_user_row"] = user
            _adopt_rust_snapshot(session, stage["history"] + [user])
        yield
    finally:
        lease.stop_refresher()
        lease.join_threads()
        lease.release()
        if stage and stage.get("retire_agent"):
            with session["history_lock"]:
                if session.get("agent") is agent:
                    session["agent"] = None
                    session["agent_ready"] = threading.Event()
                    session.pop("agent_build_started", None)
                    session.pop("agent_error", None)
                    session.pop("config_model_seen", None)
            agent._end_session_on_close = False
            with server._session_profile_runtime_scope(session, hydrate_secrets=False):
                agent.close()


def _accepted_rust_user(session: dict, stage: dict) -> dict:
    """Reuse the parent's trusted send row, with its durable identity intact."""
    from agent.context_compressor import _DB_PERSISTED_MARKER
    from tui_gateway import server

    staged = session.get("_submit_user_row")
    if isinstance(staged, dict) and staged.get("content") == stage["text"]:
        if server._submit_row_owner_key(staged, session) != stage["session_key"]:
            raise RustTurnConflict("submitted row belongs to another session")
        user = copy.deepcopy(staged)
    else:
        user = {"role": "user", "content": stage["text"]}
    if stage["user_api_content"] is not None:
        user["api_content"] = stage["user_api_content"]
    if stage["display_kind"]:
        user["display_kind"] = stage["display_kind"]
    if stage["display_metadata"]:
        user["display_metadata"] = stage["display_metadata"]
    if user.get(_DB_PERSISTED_MARKER) and any(
        user.get(key) != staged.get(key)
        for key in ("content", "api_content", "display_kind", "display_metadata")
    ):
        # The public append rewrites the addressed row under the same lease
        # guard and refreshes its canonical snapshot after adding API notes.
        user.pop(_DB_PERSISTED_MARKER)
    return user


def _append_rust_rows(session: dict, stage: dict, rows: list[dict]) -> None:
    """Use the store's public atomic append and canonical identity handoff."""
    from agent.context_compressor import _DB_PERSISTED_MARKER
    from agent.transcript_repair import sync_flushed_message_markers
    from tui_gateway import server

    agent = session["agent"]
    holder = getattr(agent, "_active_session_turn_lease_holder", None)
    if not holder:
        raise RustTurnConflict("Rust transcript has no durable turn lease")
    pending = [row for row in rows if not row.get(_DB_PERSISTED_MARKER)]
    if not pending:
        return
    with server._session_profile_runtime_scope(session, hydrate_secrets=False):
        with server._session_db(session) as db:
            if db is None or db.get_session(stage["session_key"]) is None:
                raise RustTurnConflict("session row missing at persistence")
            db.append_messages_batch(
                stage["session_key"], pending, turn_lease_holder=holder,
                turn_lease_ttl_seconds=agent._active_session_turn_lease_ttl_seconds)
    sync_flushed_message_markers(pending, pending)


def _adopt_rust_snapshot(session: dict, messages: list[dict]) -> None:
    """Keep teardown's agent snapshot on the same committed rows as the parent."""
    from agent.context_compressor import _DB_PERSISTED_MARKER
    from agent.session_persistence import _persist_lock

    agent = session["agent"]
    with _persist_lock(agent):
        agent._session_messages = messages
        watermark = next(
            (index for index, row in enumerate(messages) if not row.get(_DB_PERSISTED_MARKER)),
            len(messages))
        agent._last_flushed_db_idx = watermark
        agent._db_flush_scan_prefix = messages[:watermark]
        agent._flushed_db_message_ids = set()


def _rust_suffix(stage: dict, frame: dict) -> list[dict]:
    """Parse the full terminal transcript and retain the admitted user identity."""
    messages = frame.get("messages")
    prefix = stage["wire_history"]
    if messages is None and frame.get("type") == "turn.error":
        return copy.deepcopy(stage["rows"])
    if (not isinstance(messages, list) or messages[:len(prefix)] != prefix
            or len(messages) <= len(prefix)):
        raise RustTurnConflict("completion changed the prepared history prefix")
    wire_rows = copy.deepcopy(messages[len(prefix):])
    if wire_rows[0] != {"role": "user", "content": stage["api_text"]}:
        raise RustTurnConflict("completion must contain only the new turn suffix")
    for row in wire_rows:
        if not isinstance(row, dict) or set(row) - {
            "role", "content", "tool_calls", "tool_call_id", "tool_name",
            "reasoning", "reasoning_content", "finish_reason",
        }:
            raise RustTurnConflict("unsupported Rust transcript fields")
        if row.get("role") not in {"user", "assistant", "tool"}:
            raise RustTurnConflict("unsupported Rust transcript role")
    if any(row.get("role") == "user" for row in wire_rows[1:]):
        raise RustTurnConflict("completion contains additional user turns")
    return copy.deepcopy(stage["rows"]) + wire_rows[1:]


def _rust_terminal_status(frame: dict, rows: list[dict]) -> str:
    """Validate terminal semantics before appending any child-provided rows."""
    status = frame.get("status")
    if frame.get("type") == "turn.error":
        status = "error"
        # SIGKILL cannot run the Python child's finalizer. Its committed send
        # stays open; the bridge must use cold persisted-history recovery on
        # resubmission rather than invent a terminal transcript here.
        if frame.get("reason") == "crash":
            return status
    if frame.get("interrupted"):
        status = "interrupted"
    if status is None:
        status = "complete" if rows[-1].get("role") == "assistant" and not rows[-1].get("tool_calls") else "error"
    if status not in {"complete", "error", "interrupted"}:
        raise RustTurnConflict("invalid Rust terminal status")
    if status == "complete":
        if rows[-1].get("role") != "assistant" or rows[-1].get("tool_calls"):
            raise RustTurnConflict("successful Rust turn has no final assistant row")
        return status
    if rows[-1].get("tool_calls"):
        raise RustTurnConflict("terminal transcript is missing actual tool results")
    return status


def _apply_context_response_usage(agent, raw_usage: JsonObject | None) -> tuple[int, int]:
    """Update the compressor once for a completed native model response."""
    from agent.usage_pricing import normalize_usage

    compressor = agent.context_compressor
    if raw_usage is None:
        if compressor.awaiting_real_usage_after_compression:
            compressor.update_from_response({})
        compressor.note_usage_less_response()
        return 0, 0
    usage = normalize_usage(raw_usage, provider=agent.provider, api_mode=agent.api_mode)
    initial = raw_usage.get("initial_prompt_tokens")
    prompt_tokens = initial if type(initial) is int and initial > 0 else usage.prompt_tokens
    compressor.update_from_response({"prompt_tokens": prompt_tokens, "completion_tokens": usage.output_tokens})
    return prompt_tokens, usage.output_tokens


def prepare_context_preflight(session: dict, check: ContextPreflight) -> Literal["allow", "pressure", "unavailable"]:
    """Apply Python's pressure policy to the native worker's next logical request."""
    from agent.context_compressor import MAX_ITERATIONS_SUMMARY_REQUEST
    from agent.conversation_compression import ensure_compression_feasibility_checked
    from agent.conversation_loop import _midturn_request_pressure_tokens, _pressure_with_real_floor
    from agent.model_metadata import estimate_messages_tokens_rough
    from agent.turn_context import _agent_stale_thinking_on_wire
    from agent.usage_anchor import anchored_context_tokens, capture_usage_anchor

    with session["history_lock"]:
        stage = session.get("_rust_turn_stage")
        if not stage or stage["turn_id"] != check.turn_id or "rows" not in stage:
            raise RustTurnConflict("context preflight has no admitted turn")
        if not session.get("running") or session.get("_closing") or session.get("_turn_cancel_requested"):
            return "unavailable"
        _rust_suffix(stage, {"messages": check.messages})
        count = check.priced_prefix_count
        expected_count = stage.get("context_message_count", len(stage["wire_history"]) + 1)
        if (count != expected_count or count >= len(check.messages)
                or check.messages[count].get("role") != "assistant"
                or not check.messages[count].get("tool_calls")
                or check.messages[-1].get("role") != "tool"):
            raise RustTurnConflict("context preflight has no completed model and tool round")
        system = stage["context_system"]
        agent = session["agent"]
        prompt_tokens, completion_tokens = _apply_context_response_usage(agent, check.usage)
        anchor = (capture_usage_anchor(prompt_tokens, completion_tokens, check.messages[:count])
                  or stage.get("context_anchor"))
        stage["context_message_count"] = len(check.messages)
        stage["context_response_count"] = count
        stage["context_anchor"] = anchor

    messages = check.messages
    if check.summary:
        messages = [*messages, {"role": "user", "content": MAX_ITERATIONS_SUMMARY_REQUEST}]
    compressor = agent.context_compressor
    pressure = anchored_context_tokens(messages, anchor or getattr(agent, "_usage_anchor", None))
    anchored = pressure is not None
    if not anchored:
        api_messages = [{"role": "system", "content": system}, *messages]
        rough = estimate_messages_tokens_rough(api_messages, charge_stale_thinking=_agent_stale_thinking_on_wire(agent))
        pressure = _pressure_with_real_floor(
            compressor, _midturn_request_pressure_tokens(agent, api_messages, system, rough))
    blocked = False
    if agent.compression_enabled and getattr(agent, "max_compression_attempts", 3) > 0:
        ensure_compression_feasibility_checked(agent, pressure)
        defer = not anchored and compressor.should_defer_preflight_to_real_usage(pressure)
        blocked = not defer and compressor.should_compress(pressure)

    with session["history_lock"]:
        if (session.get("_rust_turn_stage") is not stage or not session.get("running")
                or session.get("_closing") or session.get("_turn_cancel_requested")):
            return "unavailable"
    return "pressure" if blocked else "allow"


def _pending_context_response(stage: dict, frame: dict) -> ContextResponseUsage | None:
    """Validate the terminal sample before persistence and discard a counted response."""
    from tui_gateway.contracts.compute_host import ContextResponseUsage

    if (raw := frame.get("context_response")) is None:
        return None
    response = ContextResponseUsage.model_validate(raw)
    count = response.priced_prefix_count
    if count > len(frame.get("messages") or ()):
        raise RustTurnConflict("terminal context response has an invalid priced prefix")
    if count == stage.get("context_response_count"):
        return None
    if count != stage.get("context_message_count", len(stage["wire_history"]) + 1):
        raise RustTurnConflict("terminal context response has an invalid priced prefix")
    return response


def _stage_context_recovery(session: dict, stage: dict, history: list[dict]) -> None:
    """Carry the provider's last priced boundary into the next Python turn."""
    from agent.usage_anchor import capture_usage_anchor, set_usage_anchor
    from tui_gateway import server

    session["_rust_context_recovery_pending"] = True
    anchor = stage.get("context_anchor")
    if not anchor:
        return
    appended = anchor["base_count"] - len(stage["wire_history"]) - 1
    if appended < 0:
        return
    priced_count = len(stage["history"]) + len(stage["rows"]) + appended
    if priced_count <= len(history):
        rebased = capture_usage_anchor(anchor["prompt_tokens"], anchor["completion_tokens"], history[:priced_count])
        with server._session_profile_runtime_scope(session, hydrate_secrets=False):
            set_usage_anchor(session["agent"], rebased)


def prepare_iteration_summary(session: dict, frame: dict) -> dict:
    """Build the summary from the original stored history, using the Python send projection."""
    from agent.agent_runtime_helpers import repair_message_sequence
    from agent.chat_completion_helpers import _iteration_summary_api_messages
    from agent.context_compressor import MAX_ITERATIONS_SUMMARY_REQUEST
    from agent.message_sanitization import sanitize_outbound_kwargs
    from tui_gateway import server

    with session["history_lock"]:
        stage = session["_rust_turn_stage"]
        agent = session["agent"]
        rows = _rust_suffix(stage, frame)
        rounds = sum(row["role"] == "assistant" and bool(row.get("tool_calls")) for row in rows)
        if (stage["turn_id"] != frame["turn_id"] or "summary_rows" in stage
                or rounds != agent.max_iterations or rows[-1]["role"] != "tool"):
            raise RustTurnConflict("summary does not follow the admitted iteration budget")
        stage["summary_rows"] = rows
        messages = copy.deepcopy([*stage["history"], *rows])
    with server._session_profile_runtime_scope(session):
        # Python repairs the live transcript before each ordinary request. In
        # particular, folding a model-switch marker into the user turn retires
        # its API sidecar. Preserve the separate durable rows for settlement.
        repair_message_sequence(agent, messages)
        messages.append({"role": "user", "content": MAX_ITERATIONS_SUMMARY_REQUEST})
        agent._emit_diagnostic_status(
            f"⚠️ Iteration budget exhausted ({rounds}/{agent.max_iterations}) — asking model to summarise")
        body = agent._build_api_kwargs(_iteration_summary_api_messages(agent, messages))
        sanitize_outbound_kwargs(agent, body)
        if (body.pop("extra_headers", {}) != stage["session_affinity"] or body.get("model") != agent.model
                or set(body) - _BODY_PARAMS - {"model", "tools", "messages"}
                or agent.api_key in json.dumps(body, allow_nan=False)):
            raise RustTurnConflict("unsupported iteration summary options")
        return body


def _finish_iteration_summary(session: dict, stage: dict, frame: dict, rows: list[dict]) -> None:
    """Only the parent appends the runtime nudge. Child frames cannot introduce user rows."""
    from agent.chat_completion_helpers import _EMPTY_SUMMARY_RESPONSE
    from agent.context_compressor import MAX_ITERATIONS_SUMMARY_REQUEST
    from agent.message_metadata import append_message
    from agent.turn_failure_copy import site_copy
    from agent.turn_finalizer import _explain_abnormal_exit, apply_llm_output_transform
    from tui_gateway import server

    if (frame.get("max_iterations_reached") is not True or rows != stage.get("summary_rows")
            or frame.get("status") != "complete" or frame.get("interrupted")):
        raise RustTurnConflict("invalid iteration summary completion")
    agent = session["agent"]
    with server._session_profile_runtime_scope(session, hydrate_secrets=False):
        match frame.get("summary_outcome"):
            case "success" if isinstance(frame.get("text"), str) and frame["text"].strip():
                text = frame["text"]
                if "<think>" in text:
                    text = re.sub(r"<think>.*?</think>\s*", "", text, flags=re.DOTALL).strip() or _EMPTY_SUMMARY_RESPONSE
            case "empty":
                text = _EMPTY_SUMMARY_RESPONSE
            case "error":
                text = site_copy("max_iterations_no_summary", limit=agent.max_iterations)
            case _:
                raise RustTurnConflict("unexpected iteration summary outcome")
        text, _, _ = apply_llm_output_transform(agent, text, turn_id=stage["turn_id"], logger=server.logger)
        append_message(rows, {"role": "user", "content": MAX_ITERATIONS_SUMMARY_REQUEST})
        append_message(rows, {"role": "assistant", "content": text})
        limit = agent.max_iterations
        reason = f"max_iterations_reached({limit}/{limit})"
        frame["text"] = _explain_abnormal_exit(agent, text, reason, False, server.logger)
        frame["turn_exit_reason"] = reason


def _close_rust_outcome(session: dict, frame: dict, rows: list[dict], status: str) -> None:
    """Close only after actual rows received their normal per-row identities."""
    from agent.conversation_loop import _failed_turn_display_metadata
    from agent.context_compressor import _DB_PERSISTED_MARKER
    from agent.agent_runtime_helpers_placeholders import hidden_interrupt_placeholder_row
    from agent.message_metadata import append_message
    from agent.message_sanitization import close_interrupted_tool_sequence
    from agent.turn_failure_copy import FAILED_TURN_DISPLAY_KIND, OPEN_TURN_TAIL_ROLES, failed_turn_notice

    if status == "complete" or (
        frame.get("type") == "turn.error" and frame.get("reason") == "crash"
    ):
        return
    if status == "interrupted":
        close_interrupted_tool_sequence(rows, stopped=True)
        if (rows[-1].get("role") == "assistant" and not rows[-1].get("content")
                and not rows[-1].get("tool_calls") and rows[-1].get("display_kind") != "hidden"):
            rows[-1].update(
                hidden_interrupt_placeholder_row(), display_metadata={"interrupted": True})
            rows[-1].pop(_DB_PERSISTED_MARKER, None)
        if rows[-1].get("role") == "assistant" and rows[-1].get("content"):
            metadata = {**(rows[-1].get("display_metadata") or {}), "interrupted": True}
            if rows[-1].get("display_metadata") != metadata:
                rows[-1]["display_metadata"] = metadata
                rows[-1].pop(_DB_PERSISTED_MARKER, None)
    if rows[-1].get("role") in OPEN_TURN_TAIL_ROLES:
        boundary = {
            "role": "assistant", "content": failed_turn_notice(rows),
            "display_kind": FAILED_TURN_DISPLAY_KIND,
        }
        result = {
            "error": frame.get("error") or frame.get("message") or "",
            "failure_reason": frame.get("failure_reason") or "host_error",
            "failure_retryable": frame.get("failure_retryable", False),
        }
        if metadata := _failed_turn_display_metadata(session["agent"], result):
            boundary["display_metadata"] = metadata
        append_message(rows, boundary)


def persist_rust_turn(session: dict, frame: dict) -> dict | None:
    """Persist validated terminal rows and return their public receipt.

    The bridge calls for ``turn.end`` AND attached
    ``turn.error`` before releasing rust_turn_lease, outside history_lock while
    running is true. A process crash retains the committed user for cold
    rehydration. Other failures close the accepted turn with the shared typed
    failure row; no model text or tool result is reconstructed.
    A terminal receipt never turns an error/interrupt into successful delivery.
    """
    from tui_gateway import server

    with session["history_lock"]:
        stage = session.get("_rust_turn_stage")
        if (not stage or not session.get("running")
                or session.get("session_key") != stage["session_key"]
                or int(session.get("history_version", 0)) != stage["history_version"]
                or session.get("history", []) != stage["history"]
                or frame.get("request_id") != stage["request_id"]
                or frame.get("session_key", stage["session_key"]) != stage["session_key"]
                or frame.get("turn_id", stage["turn_id"]) != stage["turn_id"]):
            raise RustTurnConflict("stale Rust turn completion")
        if "rows" not in stage:
            raise RustTurnConflict("Rust turn was not durably admitted")
        rows = _rust_suffix(stage, frame)
        context_response = _pending_context_response(stage, frame)
        if frame.get("max_iterations_reached") or frame.get("summary_outcome") is not None:
            _finish_iteration_summary(session, stage, frame, rows)
        status = _rust_terminal_status(frame, rows)
        # An interrupted assistant tail needs its hidden/API or partial
        # presentation before its first insert; settled rows are immutable.
        actual = rows[:-1] if status == "interrupted" and rows[-1].get("role") == "assistant" else rows
        _append_rust_rows(session, stage, actual)
        _close_rust_outcome(session, frame, rows, status)
        _append_rust_rows(session, stage, rows)
        if context_response is not None:
            with server._session_profile_runtime_scope(session, hydrate_secrets=False):
                _apply_context_response_usage(session["agent"], context_response.usage)
        history = [*stage["history"], *rows]
        _adopt_rust_snapshot(session, history)
        receipt_state = server._TurnRun(
            agent=session["agent"], one_turn_restore=None, terminal_callback=None,
            receipt_committed=False)
        receipt_state.history = stage["history"]
        receipt_state.compression_count = stage["compression_count"]
        receipt_state.result = {"messages": history}
        # The agent's real persistence cursor governs eligibility. Never
        # manufacture one merely because the parent appended a known suffix.
        receipt = server._persisted_turn_receipt(
            receipt_state,
            frame.get("text", rows[-1].get("content", "") if status == "complete" else ""),
            status)
        if frame.get("type") == "turn.error" and frame.get("reason") == "crash":
            stage["retire_agent"] = True
            session.pop("_rust_parent_history", None)
        else:
            session["history"] = history
            session["history_version"] = stage["history_version"] + 1
            session["_rust_parent_history"] = True
            if status == "error" and frame.get("context_failure") in {
                "context_pressure", "context_preflight_unavailable",
            }:
                _stage_context_recovery(session, stage, history)
        session.pop("_submit_user_row", None)
        session.pop("_rust_turn_stage", None)
        return receipt
