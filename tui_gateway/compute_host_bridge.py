"""Compute-host (turn isolation) bridge: relay prompts/controls to the child process and
mirror its metadata/clarify/compress acks back into the session. Bodies are rebound onto
server.py's globals at install time (method_ctx.bind_module), so they use them bare."""

from __future__ import annotations

import contextlib
import threading

from .method_ctx import HandlerRegistry, bind_module

_registry = HandlerRegistry()

_compute_host_supervisor = None
_compute_host_supervisor_lock = threading.Lock()
# Cap on how long session.compress blocks its RPC on the compute host. Must stay
# below the desktop's SESSION_COMPRESS_TIMEOUT_MS (660s) so the client gets the
# `pending` answer, not its own timeout; the late-ack path covers anything slower.
# See #97948.
_COMPUTE_HOST_COMPRESS_WAIT_CAP_SECS = 630.0


def _turn_isolation_enabled(cfg: dict | None = None) -> bool:
    if os.environ.get("HERMES_COMPUTE_HOST_CHILD") == "1":
        return False
    return bool((cfg or _load_dashboard_process_isolation_config()).get("turn_isolation"))


def _session_uses_compute_host(session: dict, cfg: dict | None = None) -> bool:
    # Routes lazy sessions whose AIAgent was never built in-process; already-built
    # sessions keep the in-process path unless a prior isolated turn marked host ownership.
    if session.get("_compute_host_turn_id"):
        return True
    python_owner = bool(session.get("_compute_host_active"))
    return _turn_isolation_enabled(cfg) and (
        python_owner or (session.get("agent") is None and session.get("agent_ready") is not None))


def _get_compute_host_supervisor(cfg: dict | None = None):
    global _compute_host_supervisor
    isolation_cfg = cfg or _load_dashboard_process_isolation_config()
    with _compute_host_supervisor_lock:
        if _compute_host_supervisor is None:
            from tui_gateway.host_supervisor import HostSupervisor
            _compute_host_supervisor = HostSupervisor(
                rpc_sink=_relay_compute_host_rpc,
                heartbeat_secs=int(isolation_cfg.get("compute_host_heartbeat_secs") or 15),
                respawn_max=int(isolation_cfg.get("compute_host_respawn_max", 3)))
        return _compute_host_supervisor


def _submit_uses_compute_host(session: dict, cfg: dict | None = None) -> bool:
    hosted = _session_uses_compute_host(session) if cfg is None else _session_uses_compute_host(session, cfg)
    if hosted and not session.get("_rust_parent_history"):
        return True
    cfg = cfg or _load_dashboard_process_isolation_config()
    return cfg.get("compute_host_impl") == "rust" and _turn_isolation_enabled(cfg)


def _session_compute_host(session: dict):
    return session.get("_compute_host_turn_host") or _get_compute_host_supervisor()


def _retire_session_rust_host(session: dict) -> None:
    with _history_lock(session):
        host = session.pop("_rust_host", None)
    if host is not None:
        host.shutdown()
        # A terminal callback may already have left the supervisor's pending table.
        with host.settlement_lock:
            pass


def _restore_compute_host_history(session: dict) -> bool:
    """Load the current Python child's durable transcript before taking it back."""
    with session["history_lock"]:
        if session.get("_closing"):
            return False
        key = session.get("session_key")
        try:
            with _session_db(session) as db:
                if db is None or db.get_session(key) is None:
                    return False
                history = db.get_messages_as_conversation(key, repair_alternation=True, include_row_ids=True)
        except Exception:
            logger.debug("Could not load hosted transcript for parent dispatch", exc_info=True)
            return False
        session["history"] = history
        agent = session.get("agent")
        if agent is not None:
            session["agent"] = None
            session["agent_ready"] = threading.Event()
            session.pop("agent_build_started", None)
            session.pop("agent_error", None)
    if agent is not None:
        agent._end_session_on_close = False
        with _session_profile_runtime_scope(session):
            agent.close()
    return True


def _prepare_inline_after_compute_host(session: dict) -> bool:
    """Return ownership to the parent only after recovering its transcript."""
    host = session.get("_rust_host")
    if host is not None and not host._stopped_respawning:
        _retire_session_rust_host(session)
    if session.get("_compute_host_active") and not _restore_compute_host_history(session):
        return False
    with session["history_lock"]:
        if session.get("_closing") or session.get("_turn_cancel_requested"):
            return False
        session.pop("_compute_host_active", None)
        session.pop("_metadata_mirror", None)
    return True


def _compute_host_turn_frame(
    rid: str, sid: str, session: dict, text: Any, image_paths: list[str] | None = None,
    queued_prompt_generation: int | None = None, display_kind: str | None = None,
    display_metadata: dict | None = None) -> dict:
    with session["history_lock"]:
        history = list(session.get("history", []))
        history_version = int(session.get("history_version", 0))
        attached_images = list(image_paths if image_paths is not None else session.get("attached_images", []))
    return {
        "type": "turn.start", "sid": sid, "request_id": rid,
        "session_key": session.get("session_key") or sid, "text": text,
        **({"display_kind": display_kind} if display_kind else {}), "history": history,
        **({"display_metadata": display_metadata} if display_metadata else {}),
        "history_version": history_version, "cols": int(session.get("cols", 80) or 80),
        "cwd": _session_cwd(session),
        # Workspace provenance rides with the cwd: a sandbox backend registers an unpicked host cwd as nothing.
        "explicit_cwd": bool(session.get("explicit_cwd")),
        "context_cwd_is_launch_artifact": _context_cwd_is_launch_artifact(session),
        "profile_home": session.get("profile_home") or "",
        "model_override": session.get("model_override"),
        # A model switch queued while the session was busy (config.set model ->
        # pending_model_switch) must cross the process boundary — the live agent is in
        # the compute host, and the server-side _apply_pending_model_switch would
        # pop-and-drop it against agent=None. COPIED (not popped): the stash is
        # cleared only after a successful isolated turn (_on_compute_host_turn_done),
        # so the fail-open in-process path can still apply it if the host dispatch fails.
        "pending_model_switch": session.get("pending_model_switch"),
        "reasoning_config_override": session.get("create_reasoning_override"),
        "service_tier_override": session.get("create_service_tier_override"),
        "source": _session_source(session), "attached_images": attached_images,
        "auth_user_id": _session_auth_user_id(session),
        "queued_prompt_generation": queued_prompt_generation,
        # #101416: vouch that this process already holds the registry lease for this session, so
        # the child adopts it as an inert token instead of re-claiming and being fenced out by
        # our own entry ("Session ... already has a live owner"). No lease held = no vouch, and
        # the child keeps its legacy self-claim path (fail-closed refusal on conflict).
        "active_session_lease": _active_session_lease_vouch(session)}


def _active_session_lease_vouch(session: dict) -> dict | None:
    """``{lease_id, session_id}`` of the REAL registry lease this process holds for the session's
    current stored id, else None. Qualified, not a bare bool: after a compression rotation a lease
    still keyed on the old id must not let a (replacement) child borrow the continuation."""
    lease = session.get("active_session_lease")
    if lease is None or getattr(lease, "released", False) or not getattr(lease, "enabled", False):
        return None
    if str(lease.session_id) != str(session.get("session_key") or ""):
        return None
    return {"lease_id": str(lease.lease_id), "session_id": str(lease.session_id)}


def _metadata_mirror(session: dict | None) -> dict:
    mirror = (session or {}).get("_metadata_mirror")
    return mirror if isinstance(mirror, dict) else {}


def _compute_host_session_info(session: dict) -> dict:
    return _session_info(session.get("agent"), session)


def _compute_host_adopt_frame_meta(session: dict, frame: dict) -> None:
    """Adopt a host frame's session_key / history_version. Caller holds history_lock.

    A rotated ``session_key`` means the child compressed A->B. The child only holds an inert
    borrowed token (``_install_borrowed_lease``), so the REAL registry lease is re-anchored here,
    by its owner — never claimed from the child pid (authority stays singular, #103737 review)."""
    new_key = str(frame.get("session_key") or "")
    if new_key and new_key != str(session.get("session_key") or ""):
        if not _transfer_active_session_slot(str(frame.get("sid") or ""), session, new_session_id=new_key):
            logger.warning("Compression session lease did not re-anchor: sid=%s old_session_id=%s new_session_id=%s",
                           frame.get("sid"), session.get("session_key"), new_key)
        session["session_key"] = new_key
    if frame.get("history_version") is not None:
        with contextlib.suppress(Exception):
            session["history_version"] = max(int(session.get("history_version", 0)),
                                             int(frame.get("history_version") or 0))


def _relay_compute_host_rpc(message: dict) -> bool:
    """Relay host frames to the client while mirroring the server→client request the host has open, so a
    reconnecting client gets it back through ``open_requests``."""
    params = message.get("params") if isinstance(message, dict) else None
    if isinstance(message, dict) and message.get("method") == "compute_host.activity":
        if isinstance(params, dict):
            session = _sessions.get(str(params.get("session_id") or ""))
            if session is not None:
                with _history_lock(session):
                    if (session.get("running") and params.get("turn_id")
                            and session.get("_compute_host_turn_id") == params["turn_id"]):
                        session["_compute_host_activity_ns"] = params.get("activity_ns")
        return True  # Internal observation, not a client event or replay entry.
    if (isinstance(message, dict) and message.get("method") == "event" and isinstance(params, dict)
            and not params.get("session_id")):
        # A session-less (global) event the child could not deliver itself: ``write_json`` would drop it
        # on this process's stdio; fan it out to every connected client like a local broadcast.
        _broadcast_global_event(str(params.get("type") or ""), params.get("payload"))
        return True
    if isinstance(message, dict) and isinstance(message.get("id"), str) and message.get("method") not in (None, "event"):
        # A server request minted by the child: remember it against its session until it is answered/withdrawn.
        session = _sessions.get(str((params or {}).get("session_id") or "")) if isinstance(params, dict) else None
        if session is not None:
            with _history_lock(session):
                session["_compute_host_open_request"] = {
                    "id": message["id"], "method": message["method"], "params": dict(params)}
    elif isinstance(params, dict) and params.get("type") == "request.cancel":
        session = _sessions.get(str(params.get("session_id") or ""))
        payload = params.get("payload")
        if session is not None and isinstance(payload, dict):
            with _history_lock(session):
                if _open_request_matches(session, payload.get("id")):
                    session.pop("_compute_host_open_request", None)
    return write_json(message)


def _history_lock(session: dict):
    """Use the live session's lock when the record has one."""
    return session.get("history_lock") or contextlib.nullcontext()


def _open_request_matches(session: dict, request_id) -> bool:
    """Whether ``session``'s mirrored open request is ``request_id``. Caller holds history_lock."""
    mirrored = session.get("_compute_host_open_request")
    return isinstance(mirrored, dict) and mirrored.get("id") == request_id


def _compute_host_request_session(request_id: str) -> tuple[str, dict] | None:
    """Find the parent mirror for one host-owned server request."""
    for sid, session in list(_sessions.items()) if request_id else ():
        with _history_lock(session):
            if _open_request_matches(session, request_id):
                return sid, session
    return None


def _relay_compute_host_response(frame: dict) -> bool:
    """Forward a client's response frame to the compute-host child that owns the request. False when no
    child owns that id."""
    located = _compute_host_request_session(str(frame.get("id") or ""))
    if located is None or not _session_uses_compute_host(located[1]):
        return False
    sid, session = located
    with _history_lock(session):
        session.pop("_compute_host_open_request", None)
    try:
        _session_compute_host(session).respond(sid, {"frame": dict(frame)})
    except Exception:
        logger.debug("compute-host response relay failed sid=%s", sid, exc_info=True)
    return True


def _cancel_session_requests(sid: str, reason: str, *, method: str) -> None:
    """Withdraw *sid*'s open *method* requests wherever they wait: in this process, or in the compute-host
    child that minted them (the parent holds only a mirror, so a local cancel never reaches that wait).
    The child's ``request.cancel`` comes back through ``_relay_compute_host_rpc``, which drops the mirror."""
    from tui_gateway import server_requests
    server_requests.cancel(sid, reason, method=method)
    session = _sessions.get(sid)
    if session is None:
        return
    with _history_lock(session):
        mirrored = session.get("_compute_host_open_request")
        hosted = isinstance(mirrored, dict) and mirrored.get("method") == method
    if not hosted or not _session_uses_compute_host(session):
        return
    try:
        _session_compute_host(session).respond(sid, {"cancel": {"method": method, "reason": reason}})
    except Exception:  # health: allow BLE001 -- the bridge raises transport-specific errors unknown here; the child's own deadline still ends the wait
        logger.warning("compute-host request cancel failed sid=%s method=%s", sid, method, exc_info=True)


def _lock_compute_host_clarify(rid: str, request_id: str, question_id: str, answer: str | None) -> dict | None:
    """Proxy a batch-clarify lock into the child that owns the request; keeps the parent mirror's locked
    answers current for reconnect snapshots. None when the request is not host-owned."""
    located = _compute_host_request_session(request_id)
    if located is None or not _session_uses_compute_host(located[1]):
        return None
    sid, session = located
    try:
        ack = _session_compute_host(session).respond(
            sid, {"lock": {"request_id": request_id, "question_id": question_id, "answer": answer}})
    except Exception as exc:
        return _err(rid, 5019, f"compute-host clarify lock failed: {exc}")
    if ack.get("type") == "respond.error":
        return _err(rid, 5019, str(ack.get("message") or "compute-host clarify lock failed"))
    response = ack.get("response")
    if not isinstance(response, dict):
        return _err(rid, 5019, "compute-host clarify lock returned an invalid response")
    if "error" in response:
        error = response["error"] if isinstance(response["error"], dict) else {}
        return _err(rid, int(error.get("code") or 5000), str(error.get("message") or "clarify lock failed"))
    result = response.get("result") if isinstance(response.get("result"), dict) else {}
    with _history_lock(session):
        if _open_request_matches(session, request_id):
            mirrored = session["_compute_host_open_request"]
            if result.get("status") == "expired" or result.get("remaining") == []:
                session.pop("_compute_host_open_request", None)
            else:
                mirrored["params"]["answers"] = {**(mirrored["params"].get("answers") or {}), question_id: answer}
    return _ok(rid, result)


def _apply_compute_host_metadata_mirror(session: dict, frame: dict | None) -> None:
    """Mirror host-owned session metadata: under turn isolation the host is the only
    writer of live agent/history state, and UI reads must not build a second agent."""
    if not isinstance(frame, dict):
        return
    with _history_lock(session):
        _compute_host_adopt_frame_meta(session, frame)
        if frame.get("message_count") is not None:
            with contextlib.suppress(Exception):
                session["_metadata_message_count"] = int(frame.get("message_count") or 0)
    info = frame.get("session_info")
    if isinstance(info, dict):
        session["_metadata_mirror"] = {**_metadata_mirror(session), **info}
        session["_metadata_mirror_updated_at"] = time.time()


_NO_FORWARDED_SWITCH = object()


def _on_compute_host_turn_done(
    rid: str, sid: str, session: dict, frame: dict, *, forwarded_switch=_NO_FORWARDED_SWITCH,
    crash_info: dict | None = None, goal_followup: str | None = None,
) -> None:
    _apply_compute_host_metadata_mirror(session, frame)
    with session["history_lock"]:
        rust_crashed = frame.get("reason") == "crash" and session.get("_compute_host_turn_host") is not None
        session.pop("_rust_turn_stage", None)
        session.pop("_rust_pre_model_info", None)
        session.pop("_rust_ready_info", None)
        session.pop("_compute_host_turn_id", None)
        session.pop("_compute_host_turn_host", None)
        session.pop("_compute_host_activity_ns", None)
        session["running"] = False
        session["last_active"] = time.time()
        if frame.get("status") == "error" and frame.get("error"):
            _fail_inflight_turn(session, frame["error"], error_surface=frame.get("error_surface"))
        else:
            _clear_inflight_turn(session)
        session.pop("_compute_host_open_request", None)
        # A switch submitted after the slot becomes idle belongs to the next turn.
        if frame.get("type") != "turn.error" and (
                forwarded_switch is _NO_FORWARDED_SWITCH or session.get("pending_model_switch") is forwarded_switch):
            session.pop("pending_model_switch", None)
        # The retained agent is the planner. Until the replacement starts its
        # next turn, no child owns a live execution inventory to advertise.
        info_session = {**session, "_compute_host_active": True} if rust_crashed else session
    _release_deferred_active_session_lease(session)
    if session.get("_closing"):
        return
    if frame.get("type") == "turn.error":
        message = str(frame.get("message") or "compute host turn failed")
        _emit("message.complete", sid, {"text": f"Error: {message}", "status": "error"})
    if not frame.get("session_info_emitted"):
        _emit("session.info", sid, crash_info if crash_info is not None else _compute_host_session_info(info_session))
    if goal_followup:
        _run_post_turn_followups(rid, sid, session, None, goal_followup)
    else:
        _drain_queued_prompt(rid, sid, session)


def _submit_prompt_to_compute_host(
    rid: str, sid: str, session: dict, text: Any, image_paths: list[str] | None = None,
    queued_prompt_generation: int | None = None, display_kind: str | None = None,
    display_metadata: dict | None = None) -> dict:
    cfg = _load_dashboard_process_isolation_config()
    frame = _compute_host_turn_frame(rid, sid, session, text, image_paths=image_paths,
                                     queued_prompt_generation=queued_prompt_generation,
                                     display_kind=display_kind, display_metadata=display_metadata)
    # Caller JSON-RPC ids may repeat across sockets and turns. Use an opaque
    # dispatch lifetime token, installed before a fast child can send activity.
    from tui_gateway.rust_host_supervisor import RustHostSupervisor, select_host
    from tui_gateway.rust_turn_plan import persist_rust_turn

    leases = contextlib.ExitStack()
    turn_id = frame["turn_id"] = frame["request_id"] = uuid.uuid4().hex
    with session["history_lock"]:
        if session.get("_closing") or session.get("_turn_cancel_requested"):
            return _err(rid, 5019, "compute-host turn cancelled before dispatch")
        previous_python_owner = session.get("_compute_host_active")
        previous_mirror = session.get("_metadata_mirror")
        session["_compute_host_turn_id"] = turn_id
        session.pop("_compute_host_activity_ns", None)

    def _complete(done: dict) -> None:
        # submit_turn reports a synchronous pipe failure via the callback before re-raising;
        # leave the session untouched so prompt.submit can fail open to the in-process path.
        if done.get("reason") == "send_failed":
            return
        with session["history_lock"]:
            owns_turn = session.get("_compute_host_turn_id") == turn_id
        if not owns_turn:
            leases.close()
            return
        crash_info = None
        goal_followup = None
        try:
            if rust_host is not None:
                terminal = {"type": done["type"], "message": done.get("message"), "reason": done.get("reason")}
                with rust_host.settlement_lock:
                    persisted = False
                    receipt = None
                    handoff = None
                    if done.get("type") == "turn.error" and done.get("reason") == "crash":
                        # Settlement may retire the planner on lease exit. Capture
                        # its route now using the projection for an absent child.
                        try:
                            with session["history_lock"]:
                                crash_view = {**session, "_compute_host_active": True,
                                              "running": False, "inflight_turn": None}
                                # Keep the route from the planner, while the
                                # absent child has no active prompt to report.
                                crash_view["_metadata_mirror"] = {**_metadata_mirror(session), "system_prompt": ""}
                            with _session_profile_runtime_scope(session, hydrate_secrets=False):
                                crash_info = _compute_host_session_info(crash_view)
                        except Exception:
                            logger.debug("Could not capture Rust crash metadata for sid=%s", sid, exc_info=True)
                    try:
                        with session["history_lock"]:
                            attached = session.get("_rust_host") is rust_host
                        if attached and session.get("_rust_turn_stage"):
                            details = rust_host.terminal_details(done)
                            settlement = {**{key: value for key, value in done.items() if key != "provider_error"},
                                          **details}
                            receipt = persist_rust_turn(session, settlement)
                            if (db := getattr(session.get("agent"), "_session_db", None)) is not None:
                                db.flush_token_counts()
                            if settlement.get("max_iterations_reached"):
                                handoff = {"completed": False, "failed": False, "interrupted": False,
                                           "turn_exit_reason": settlement["turn_exit_reason"],
                                           "final_response": settlement["text"]}
                            if details.get("status") == "error":
                                terminal.update(details)
                            persisted = True
                    except Exception:
                        if done.get("type") == "turn.end":
                            terminal = {"type": "turn.error", "message": "Could not save the Rust turn transcript"}
                        else:
                            logger.debug("Could not persist terminal Rust error for sid=%s", sid, exc_info=True)
                    finally:
                        # Only the parent may advance Rust transcript metadata.
                        done = terminal
                        try:
                            with _session_profile_runtime_scope(session, hydrate_secrets=False):
                                rust_host.finish_events(
                                    persisted=persisted and done.get("type") == "turn.end"
                                    and not session.get("_closing"), receipt=receipt, handoff=handoff)
                                if persisted and handoff is not None and not session.get("_closing"):
                                    goal_followup = _goal_followup_after_turn(
                                        sid, session, handoff, "complete", handoff["final_response"])
                                    _publish_session_control_snapshot(sid, session, only_if_present=True)
                        finally:
                            leases.close()
        finally:
            with _session_profile_runtime_scope(session, hydrate_secrets=False):
                _on_compute_host_turn_done(
                    rid, sid, session, done, forwarded_switch=frame.get("pending_model_switch"),
                    crash_info=crash_info, goal_followup=goal_followup)

    rust_host = None
    claimed_images = []
    try:
        host = select_host(sid, session, cfg, frame, leases)
        rust_host = host if isinstance(host, RustHostSupervisor) else None
        with session["history_lock"]:
            if (host is None or session.get("_closing") or session.get("_turn_cancel_requested")
                    or (queued_prompt_generation is not None
                        and queued_prompt_generation != int(session.get("_queued_prompt_generation", 0)))):
                raise RuntimeError("compute host is unavailable for this dispatch")
            session["_compute_host_active"] = rust_host is None
            if rust_host is not None:
                session["_compute_host_turn_host"] = rust_host
                session.pop("_metadata_mirror", None)
            if image_paths is None:
                claimed_images = frame["attached_images"]
                del session.setdefault("attached_images", [])[:len(claimed_images)]
        host.submit_turn(frame, on_complete=_complete)
    except Exception:
        leases.close()
        with session["history_lock"]:
            if session.get("_compute_host_turn_id") == turn_id:
                session.pop("_rust_turn_stage", None)
                session.pop("_rust_pre_model_info", None)
                session.pop("_rust_ready_info", None)
                session.pop("_compute_host_turn_id", None)
                session.pop("_compute_host_turn_host", None)
                session.pop("_compute_host_activity_ns", None)
                session["_compute_host_active"] = bool(previous_python_owner)
                if previous_mirror is not None:
                    session["_metadata_mirror"] = previous_mirror
                if claimed_images:
                    session["attached_images"] = claimed_images + session.get("attached_images", [])
        _release_deferred_active_session_lease(session)
        return _err(rid, 5019, "compute-host dispatch failed before turn acceptance")
    return _ok(rid, {"status": "streaming", "turn_isolation": True})


def _send_compute_host_control(
    sid: str, *, route_name: str, command: str = "", payload: dict | None = None,
    wait: bool = True, timeout: float = 30.0, on_late_ack=None) -> dict:
    frame = dict(payload or {})
    frame.setdefault("type", "control")
    frame.setdefault("command", command)
    return _session_compute_host(_sessions.get(sid) or {}).control(
        sid, route_name=route_name, payload=frame, wait=wait, timeout=timeout,
        on_late_ack=on_late_ack)


def _compute_host_ack_error(rid, ack: dict, code: int, default: str):
    """``_err`` for a ``control.error``/``error`` ack, else None."""
    if ack.get("type") in {"control.error", "error"}:
        return _err(rid, code, str(ack.get("message") or default))
    return None


def _compute_host_compress_wait_seconds(cfg: dict | None = None) -> float:
    """RPC wait budget for a compute-host compress control: the configured compression
    ceiling plus slack, capped below the desktop's RPC timeout (a fixed waiter reported
    false timeouts while the host kept working); slower acks land via the late-ack path.

    See #97948.
    """
    from agent.conversation_compression import resolve_context_compression_timeouts
    try:
        compression_cfg = (cfg if cfg is not None else _load_cfg()).get("compression", {})
    except Exception:
        compression_cfg = {}
    if not isinstance(compression_cfg, dict):
        compression_cfg = {}
    _idle, ceiling = resolve_context_compression_timeouts(compression_cfg)
    return float(min(max(ceiling + 30.0, 120.0), _COMPUTE_HOST_COMPRESS_WAIT_CAP_SECS))


def _adopt_late_compute_host_compress_ack(sid: str, session: dict, ack: dict, *, route_name: str) -> None:
    """Adopt a compress ack that arrived after its RPC waiter answered ``pending``: the only place
    the rotated session_key / history_version / mirror can land and the client's only signal
    (the same ``session.info`` + ``compacted`` edges the in-process /compress path emits). A late
    ``control.error`` goes out via ``error``."""
    with _sessions_lock:
        if _sessions.get(sid) is not session:
            return
    if not isinstance(ack, dict) or ack.get("type") in {"control.error", "error"}:
        message = str((ack or {}).get("message") or f"compute-host {route_name} failed")
        _emit("error", sid, {"message": f"compression failed: {message}"})
        _status_update(sid, "ready")
        return
    _apply_compute_host_metadata_mirror(session, ack)
    _emit("session.info", sid, _compute_host_session_info(session))
    _status_update(sid, "compacted", "✓ Context compression complete")


def register(server) -> None:
    """Publish this module's helpers + handlers onto ``server``, rebound to its globals."""
    bind_module(globals(), server, skip=("_",))
