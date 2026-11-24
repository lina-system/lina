"""Own the process, private file policy and client events of one Rust-hosted session."""

from __future__ import annotations

import json
import os
import random
import re
import threading
from pathlib import Path
from weakref import WeakKeyDictionary

from pydantic import ValidationError

from tui_gateway.host_supervisor import HostSupervisor

_TURN_EVENTS = frozenset({
    "message.start", "message.delta", "message.interim", "message.complete",
    "reasoning.delta", "thinking.delta", "tool.generating", "tool.start", "tool.complete",
})


class _ProviderFailure(Exception):
    def __init__(self, status_code: int, detail: str):
        self.status_code = status_code
        self.body = {"error": {"message": detail}}
        super().__init__(detail)


class RustHostSupervisor(HostSupervisor):
    def __init__(self, sid, session, *, home, credentials, endpoint, cfg):
        if not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", sid):
            raise ValueError("invalid compute host session id")
        self.sid = sid
        self.session = session
        self.endpoint = endpoint
        self.settlement_lock = threading.Lock()
        self._retired = False
        self._event_source = None
        self._final_event = None
        self._failure_status = None
        self._read_grant = None
        self._stdout_lock = threading.Lock()
        self._stdout_drained = WeakKeyDictionary()
        self._started_child = False
        self._replacement_ready = False
        super().__init__(
            argv=["lina-turn-host"],
            registry_path=Path(home) / "state" / f"dashboard-rust-host-{sid}.json",
            expected_hermes_home=home,
            env={"HERMES_HOME": home, "OPENAI_API_KEY": credentials["OPENAI_API_KEY"]},
            rpc_sink=self._receive_supervisor_error,
            heartbeat_secs=cfg["compute_host_heartbeat_secs"],
            respawn_max=cfg["compute_host_respawn_max"],
            autostart=False,
        )

    def _child_environment(self):
        # The default supervisor boots a Python agent which hydrates its own profile.
        # This process gets only the credentials resolved for its one model endpoint.
        return {"PATH": os.environ.get("PATH", os.defpath), **self.env}

    def start(self):
        with self._lock:
            if self._retired:
                raise RuntimeError("Rust compute host was retired")
            super().start()

    def _stdout_end(self, proc):
        with self._stdout_lock:
            return self._stdout_drained.setdefault(proc, threading.Event())

    def _drain_stdout(self, proc):
        try:
            super()._drain_stdout(proc)
        finally:
            self._stdout_end(proc).set()

    def _wait_for_exit(self, proc):
        proc.wait()
        if not self._closing:
            # Flush already-written progress before publishing a crash. A slow
            # parent read-policy callback cannot indefinitely hold settlement.
            self._stdout_end(proc).wait(timeout=1)
        super()._wait_for_exit(proc)

    def submit_turn(self, frame, *, on_complete=None):
        # Serialize a synchronous pipe failure with child exit and shutdown. Only
        # a successfully written start may be settled by an asynchronous callback.
        with self._lock:
            with self.session["history_lock"]:
                if (self.session.get("_closing") or self.session.get("_turn_cancel_requested")
                        or self.session.get("_compute_host_turn_host") is not self
                        or self.session.get("_compute_host_turn_id") != frame.get("turn_id")):
                    raise RuntimeError("Rust turn lost ownership before dispatch")
            return super().submit_turn(frame, on_complete=on_complete)

    def interrupt(self, sid, *, request_id=None, turn_id=None):
        with self._lock:
            if self._retired or not self.is_running():
                return
            if turn_id is None or turn_id not in self._pending_turns:
                return
            self._send_frame({
                "type": "interrupt", "sid": sid, "request_id": request_id or f"interrupt-{turn_id}",
                "turn_id": turn_id,
            })

    def _receive_activity(self, params):
        turn_id = params.get("turn_id")
        with self.session["history_lock"]:
            if (turn_id and self.session.get("running") and not self.session.get("_closing")
                    and self.session.get("_compute_host_turn_host") is self
                    and self.session.get("_compute_host_turn_id") == turn_id
                    and self._event_source == (threading.current_thread(), turn_id)):
                # Keep the child's monotonic sample so a repeated observation still ages.
                self.session["_compute_host_activity_ns"] = params.get("activity_ns")

    def _receive_event(self, message):
        from tui_gateway import server
        from tui_gateway.contracts.registry import EVENTS

        params = message.get("params")
        if not isinstance(params, dict) or params.get("session_id") != self.sid:
            return
        if message.get("method") == "compute_host.activity":
            self._receive_activity(params)
            return
        name = params.get("type")
        payload = params.get("payload")
        if (message.get("method") != "event" or not isinstance(name, str)
                or name not in _TURN_EVENTS or not isinstance(payload, dict)):
            return
        try:
            EVENTS[name].payload.model_validate_json(json.dumps(payload), strict=True)
        except ValidationError:
            return
        with self.session["history_lock"]:
            turn_id = self.session.get("_compute_host_turn_id")
            if (not turn_id or self.session.get("_compute_host_turn_host") is not self
                    or self.session.get("_closing")
                    or self._event_source != (threading.current_thread(), turn_id)
                    or ("turn_id" in payload and payload["turn_id"] != turn_id)
                    or self._final_event is not None):
                return
            if name == "message.complete":
                self._final_event = payload
                return
        if name == "thinking.delta":
            text = payload["text"]
            if text:
                from agent.display import KawaiiSpinner

                face = random.choice(KawaiiSpinner.get_thinking_faces())
                verb = random.choice(KawaiiSpinner.get_thinking_verbs())
                text = f"{face} {verb}..."
            server._agent_thinking_update(self.sid, text)
            return
        if name == "tool.generating" and not server._tool_progress_enabled(self.sid):
            return
        server._emit(name, self.sid, payload)
        if name == "message.start":
            if (info := self.session.pop("_rust_pre_model_info", None)) is not None:
                server._emit("session.info", self.sid, info)

    def _receive_supervisor_error(self, message):
        from tui_gateway import server

        with self.session["history_lock"]:
            visible = self.session.get("_compute_host_turn_host") is self and not self.session.get("_closing")
        if visible:
            server._emit("error", self.sid, message["params"]["payload"])

    def terminal_details(self, frame: dict) -> dict:
        with self.session["history_lock"]:
            payload = self._final_event or {}
        details = {key: payload[key] for key in ("text", "status", "error") if key in payload}
        if payload.get("status") == "error" and frame.get("context_failure") in {
            "context_pressure", "context_preflight_unavailable",
        }:
            return {**details, "failure_reason": frame["context_failure"], "failure_retryable": True}
        if payload.get("status") != "error" or frame.get("provider_error") is None:
            return details
        from agent.error_classifier import FailoverReason, classify_api_error
        from agent.error_surface import build_error_surface_from_result
        from agent.redact import redact_sensitive_text
        from agent.turn_failure_copy import exhausted_copy, nonretryable_copy, provider_label_for
        from tui_gateway import server
        from tui_gateway.contracts.compute_host import ProviderError

        try:
            failure = ProviderError.model_validate(frame["provider_error"], strict=True)
        except ValidationError:
            return details
        agent = self.session["agent"]
        with server._session_profile_runtime_scope(self.session, hydrate_secrets=False):
            detail = redact_sensitive_text(failure.message, force=True)
            detail = server._strip_prompt_echo(detail, (self.session.get("_rust_turn_stage") or {}).get("text"))
            if isinstance(agent.api_key, str) and agent.api_key:
                detail = detail.replace(agent.api_key, "[REDACTED]")
            error = _ProviderFailure(failure.status_code, detail)
            classifier = classify_api_error(
                error, provider=agent.provider, model=agent.model, base_url=self.endpoint, api_key=agent.api_key)
            summary = agent._summarize_api_error(error)
            result = {"error": summary, "failure_reason": classifier.reason.value,
                      "failure_retryable": classifier.retryable}
            label = provider_label_for(agent.provider)
            if classifier.retryable:
                text = exhausted_copy(classifier.reason.value, label=label, attempts=failure.attempts,
                                      summary=summary, error=error)
                if classifier.reason in (FailoverReason.rate_limit, FailoverReason.upstream_rate_limit):
                    attempts = "attempt" if failure.attempts == 1 else "retries"
                    status = f"❌ Rate limited after {failure.attempts} {attempts} — {summary}"
                else:
                    status = f"❌ API failed after {failure.attempts} retries — {summary}"
            else:
                from agent.turn_recovery import _NONRETRYABLE_LABELS

                text = nonretryable_copy(classifier, provider=agent.provider, model=agent.model, summary=summary)
                heading = _NONRETRYABLE_LABELS.get(classifier.reason, f"{label} rejected the request and retrying won't help")
                status = f"❌ {heading}: {summary}"
            surface = build_error_surface_from_result(result, provider=agent.provider, model=agent.model)
        payload.update(text=text, error=summary, recoverable=True)
        if surface is not None:
            payload["error_surface"] = surface
        self._failure_status = status
        return {**details, **result, "text": text, "error_surface": surface}

    def finish_events(self, *, persisted, receipt=None, handoff=None):
        from tui_gateway import server

        with self.session["history_lock"]:
            payload, self._final_event = self._final_event, None
            self._event_source = None
            self._read_grant = None
            failure_status, self._failure_status = self._failure_status, None
        if persisted and payload is not None:
            from tui_gateway.render import render_message

            if payload.get("status") == "interrupted" and payload.get("text") == "":
                payload["text"] = None
            if handoff is not None:
                payload["text"] = handoff["final_response"]
            if (agent := self.session.get("agent")) is not None and isinstance(usage := payload.get("usage"), dict):
                server._apply_rust_turn_usage(self.session, agent, usage)
                payload["usage"] = server._compute_host_usage(self.session, agent)
            if isinstance(payload.get("text"), str) and (
                    rendered := render_message(payload["text"], self.session.get("cols", 80))):
                payload["rendered"] = rendered
            if receipt is not None and payload.get("status") == "complete":
                payload["persisted_turn"] = receipt
            if failure_status is not None:
                from gateway.warning_notifications import DiagnosticText

                server._agent_status_update(self.sid, "lifecycle", DiagnosticText(failure_status))
            server._emit("message.complete", self.sid, payload)

    def _handle_host_frame(self, frame):
        kind = frame.get("type")
        if kind in ("turn.started", "turn.end", "turn.error") and frame.get("sid") != self.sid:
            return
        if kind == "hello":
            self._replacement_ready = self._started_child
        if kind == "turn.started":
            with self.session["history_lock"]:
                turn_id = self.session.get("_compute_host_turn_id")
                if (not turn_id or frame.get("request_id") != turn_id or self.session.get("_closing")
                        or self.session.get("_compute_host_turn_host") is not self):
                    return
                self._event_source = (threading.current_thread(), turn_id)
                info = self.session.pop("_rust_ready_info", None)
                replacement = self._replacement_ready
                self._replacement_ready = False
                self._started_child = True
            if replacement and info is None:
                from tui_gateway import server

                with server._session_profile_runtime_scope(self.session, hydrate_secrets=False):
                    live = server._compute_host_session_info(self.session)
                    info = {**live, "running": False, "turn_started_at": None}
                    self.session.setdefault("_rust_pre_model_info", live)
            if info is not None:
                from tui_gateway import server

                server._emit("session.info", self.sid, info)
            return
        if kind == "rpc":
            if isinstance(message := frame.get("message"), dict):
                self._receive_event(message)
            return
        if kind == "iteration.summary":
            self._prepare_iteration_summary(frame)
            return
        if kind == "context.preflight":
            self._prepare_context_preflight(frame)
            return
        if kind != "read_file.policy":
            return super()._handle_host_frame(frame)
        from tui_gateway import server
        from tui_gateway.rust_file_policy import handle_file_policy

        op, path = frame.get("op"), frame.get("path")
        reply = {
            "type": "read_file.policy.ack", "sid": self.sid,
            "turn_id": frame.get("turn_id"), "request_id": frame.get("request_id"), "ok": False,
        }
        with self.session["history_lock"]:
            owns_read = (
                self.session.get("_compute_host_turn_host") is self
                and self.session.get("running")
                and not self.session.get("_closing")
                and frame.get("sid") == self.sid
                and frame.get("turn_id")
                and frame["turn_id"] == self.session.get("_compute_host_turn_id")
                and self._event_source == (threading.current_thread(), frame["turn_id"])
            )
            if op == "redact":
                permitted = owns_read and self._read_grant == (
                    frame.get("turn_id"), path, frame.get("resolved"), frame.get("identity"))
                if permitted:
                    self._read_grant = None
            else:
                permitted = owns_read and op == "authorize" and not self.session.get("_turn_cancel_requested")
                if permitted:
                    self._read_grant = None
        if permitted:
            try:
                with server._session_profile_runtime_scope(self.session):
                    reply.update(handle_file_policy(self.session, frame))
                if op == "authorize" and reply["ok"]:
                    with self.session["history_lock"]:
                        if (self.session.get("_turn_cancel_requested") or self.session.get("_closing")
                                or self._event_source != (threading.current_thread(), frame["turn_id"])):
                            reply = {"type": "read_file.policy.ack", "sid": self.sid, "ok": False,
                                     "turn_id": frame["turn_id"], "request_id": frame.get("request_id")}
                        else:
                            self._read_grant = (frame["turn_id"], path, reply["resolved"], dict(reply["identity"]))
            except Exception:
                # The request can contain file content. Never put it or an exception
                # derived from it into logs or a public failure event.
                reply["ok"] = False
        self._send_frame(reply)

    def _prepare_iteration_summary(self, frame):
        from tui_gateway.rust_turn_plan import prepare_iteration_summary

        turn_id = frame.get("turn_id")
        reply = {"type": "iteration.summary.ack", "sid": self.sid, "request_id": frame.get("request_id"),
                 "turn_id": turn_id, "ok": False}
        with self.session["history_lock"]:
            owns_turn = (turn_id and frame.get("sid") == self.sid and self.session.get("running")
                         and not self.session.get("_closing") and not self.session.get("_turn_cancel_requested")
                         and self.session.get("_compute_host_turn_host") is self
                         and self.session.get("_compute_host_turn_id") == turn_id
                         and self._event_source == (threading.current_thread(), turn_id)
                         and self._final_event is None)
        if owns_turn:
            try:
                reply.update(body=prepare_iteration_summary(self.session, frame), ok=True)
            except Exception:
                # Request options and transcript text are private to this turn.
                reply["ok"] = False
        self._send_frame(reply)

    def _prepare_context_preflight(self, frame):
        from tui_gateway import server
        from tui_gateway.contracts.compute_host import ContextPreflight
        from tui_gateway.rust_turn_plan import RustTurnConflict, prepare_context_preflight

        try:
            check = ContextPreflight.model_validate(frame)
        except ValidationError:
            return  # An invalid request cannot authorize a model call.
        decision = "unavailable"
        with self.session["history_lock"]:
            owns_turn = (check.sid == self.sid and self.session.get("running")
                         and not self.session.get("_closing") and not self.session.get("_turn_cancel_requested")
                         and self.session.get("_compute_host_turn_host") is self
                         and self.session.get("_compute_host_turn_id") == check.turn_id
                         and self._event_source == (threading.current_thread(), check.turn_id)
                         and self._final_event is None)
        if owns_turn:
            try:
                with server._session_profile_runtime_scope(self.session):
                    decision = prepare_context_preflight(self.session, check)
            except RustTurnConflict:
                decision = "unavailable"
        self._send_frame({
            "type": "context.preflight.ack", "sid": self.sid, "request_id": check.request_id,
            "turn_id": check.turn_id, "decision": decision,
        })

    def shutdown(self):
        with self._lock:
            if self._retired:
                return
            self._retired = self._closing = True
        try:
            super().shutdown()
        finally:
            self._fail_pending_turns(reason="shutdown", message="compute host shut down")


def select_host(sid, session, cfg, frame, leases):
    """Prepare the eligible session before launching a child with its provider credential."""
    from hermes_constants import get_hermes_home
    from tui_gateway import server
    from tui_gateway.rust_turn_plan import prepare_rust_turn, rust_turn_lease

    rust = cfg.get("compute_host_impl") == "rust"
    if rust:
        with session["history_lock"]:
            recover_context = session.pop("_rust_context_recovery_pending", False)
        if recover_context:
            server._retire_session_rust_host(session)
            return None  # Continue inline with the settled transcript and Python compaction.

    ready = not rust or not session.get("_compute_host_active") or server._restore_compute_host_history(session)
    if rust and ready and session.get("_compute_host_active"):
        frame.update(history=list(session["history"]), history_version=session.get("history_version", 0))
    prepared = None
    if rust and ready:
        session["_rust_host_preparing"] = True
        try:
            prepared = prepare_rust_turn(sid, session, frame)
        finally:
            session.pop("_rust_host_preparing", None)
            if prepared is None:
                session.pop("_rust_pre_model_info", None)
                session.pop("_rust_ready_info", None)
    if prepared is None:
        server._retire_session_rust_host(session)
        if session.get("_rust_parent_history"):
            return None  # A previous Python child may still cache a pre-Rust transcript.
        return server._get_compute_host_supervisor(cfg)
    with session["history_lock"]:
        host = session.get("_rust_host")
    plan, env = prepared
    leases.enter_context(rust_turn_lease(session, frame))
    home = str(session.get("profile_home") or get_hermes_home())
    if host and (host.endpoint != plan["base_url"] or host.env != {"HERMES_HOME": home, **env}):
        server._retire_session_rust_host(session)
        host = None
    with session["history_lock"]:
        if session.get("_closing") or session.get("_turn_cancel_requested"):
            return None
        if host is None:
            host = RustHostSupervisor(sid, session, home=home, credentials=env, endpoint=plan["base_url"], cfg=cfg)
            session["_rust_host"] = host
    frame["turn_plan"] = plan
    return host
