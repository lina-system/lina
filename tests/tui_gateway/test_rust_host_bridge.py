"""Parent ownership and dispatch behavior with a synthetic Rust host pipe."""

from __future__ import annotations

from contextlib import contextmanager
from threading import Lock
from types import SimpleNamespace

import pytest

from hermes_constants import get_hermes_home
from tui_gateway import server
from tui_gateway.contracts.registry import EVENTS
from tui_gateway.host_supervisor import HostSupervisor
from tui_gateway.rust_host_supervisor import RustHostSupervisor


@pytest.fixture
def rust_session(tmp_path, monkeypatch):
    cfg = {"turn_isolation": True, "compute_host_impl": "rust",
           "compute_host_heartbeat_secs": 15, "compute_host_respawn_max": 1}
    session = {"session_key": "stored", "history_lock": Lock(), "running": True,
               "history": [], "profile_home": str(tmp_path)}
    host = RustHostSupervisor("sid", session, home=str(tmp_path),
                              credentials={"OPENAI_API_KEY": "synthetic-key"},
                              endpoint="http://127.0.0.1:1", cfg=cfg)
    events = []
    monkeypatch.setattr(server, "_load_dashboard_process_isolation_config", lambda: cfg)
    monkeypatch.setattr(server, "_compute_host_session_info", lambda _session: {})
    monkeypatch.setattr(server, "_emit", lambda name, _sid, payload: events.append((name, payload)))
    monkeypatch.setattr(HostSupervisor, "start", lambda _self: None)
    return session, host, events


def test_rust_child_accepts_only_current_turn_events(rust_session):
    session, host, events = rust_session
    session.update(_rust_host=host, _compute_host_turn_host=host, _compute_host_turn_id="current")

    host._handle_host_frame({"type": "turn.started", "sid": "sid", "request_id": "current"})
    for name, payload in (
        ("message.delta", {"turn_id": "previous", "text": "stale"}),
        ("message.delta", {"turn_id": "current", "text": []}),
        ("session.reclaimed", {"session_id": "sid", "stored_session_id": "stored", "reason": "idle_timeout"}),
        ("message.delta", {"turn_id": "current", "text": "visible"}),
        ("message.complete", {"text": "visible", "status": "complete"}),
    ):
        host._receive_event({"method": "event", "params": {"type": name, "session_id": "sid", "payload": payload}})

    assert events == [("message.delta", {"turn_id": "current", "text": "visible"})]
    host.finish_events(persisted=True)
    assert events[-1][0] == "message.complete"


@pytest.mark.parametrize("mode, visible", [("all", True), ("verbose", True), ("off", False)])
def test_rust_tool_generation_respects_session_progress_mode(rust_session, monkeypatch, mode, visible):
    session, host, events = rust_session
    session.update(tool_progress_mode=mode, _compute_host_turn_host=host, _compute_host_turn_id="current")
    monkeypatch.setitem(server._sessions, "sid", session)
    host._handle_host_frame({"type": "turn.started", "sid": "sid", "request_id": "current"})

    host._handle_host_frame({"type": "rpc", "message": {"method": "event", "params": {
        "type": "tool.generating", "session_id": "sid", "payload": {"name": "read_file"},
    }}})

    assert events == ([("tool.generating", {"name": "read_file"})] if visible else [])


def test_cold_rust_build_announces_idle_without_releasing_admission(rust_session, monkeypatch):
    session, host, events = rust_session
    agent = SimpleNamespace(_cached_system_prompt=None)
    session.update(
        agent=agent, inflight_turn={"started_at": 123.0}, _rust_host_preparing=True,
        _compute_host_turn_host=host, _compute_host_turn_id="current")
    monkeypatch.setattr(server, "_start_session_services", lambda *_: None)
    monkeypatch.setattr(server, "_schedule_mcp_late_refresh", lambda *_: None)
    monkeypatch.setattr(server, "_probe_config_health", lambda _: None)

    with server._session_profile_runtime_scope(session, hydrate_secrets=False):
        server._announce_built_agent("sid", "stored", session, agent)
        agent._cached_system_prompt = "assembled prompt"
        server._emit_session_info("sid", session)
    host._handle_host_frame({"type": "turn.started", "sid": "sid", "request_id": "current"})
    host._receive_event({"method": "event", "params": {
        "type": "message.start", "session_id": "sid", "payload": {"turn_id": "current"}}})

    parent, initial, started = [payload for name, payload in events if name == "session.info"]
    assert [name for name, _ in events] == ["session.info", "session.info", "message.start", "session.info"]
    assert session["running"]
    assert parent["running"] is True and parent["turn_started_at"] == 123.0
    assert initial["running"] is False and initial["turn_started_at"] is None
    assert started["running"] is True and started["turn_started_at"] == 123.0
    assert parent["system_prompt"] == initial["system_prompt"] == ""
    assert started["system_prompt"] == "assembled prompt"


def test_stop_finishes_only_the_original_authorized_read(rust_session, monkeypatch):
    from tui_gateway import rust_file_policy

    session, host, _events = rust_session
    session.update(_compute_host_turn_host=host, _compute_host_turn_id="current")
    base = {"type": "read_file.policy", "sid": "sid", "turn_id": "current", "path": "sample.txt"}
    identity = {"inode": 10}
    replies = []

    def decide(_session, frame):
        if frame["op"] == "authorize":
            return {"ok": True, "resolved": "/sample.txt", "identity": identity}
        return {"ok": True, "content": "redacted"}

    monkeypatch.setattr(host, "_send_frame", replies.append)
    monkeypatch.setattr(rust_file_policy, "handle_file_policy", decide)
    host._handle_host_frame({"type": "turn.started", "sid": "sid", "request_id": "current"})
    host._handle_host_frame({**base, "op": "authorize", "request_id": "grant"})
    session["_turn_cancel_requested"] = True
    host._handle_host_frame({**base, "op": "authorize", "request_id": "new"})
    redact = {**base, "op": "redact", "resolved": "/sample.txt", "content": "synthetic"}
    host._handle_host_frame({**redact, "identity": {"inode": 11}, "request_id": "wrong"})
    host._handle_host_frame({**redact, "identity": identity, "request_id": "finish"})
    host._handle_host_frame({**redact, "identity": identity, "request_id": "repeat"})

    assert [(reply["request_id"], reply["ok"]) for reply in replies] == [
        ("grant", True), ("new", False), ("wrong", False), ("finish", True), ("repeat", False),
    ]
    assert replies[3]["content"] == "redacted"


@pytest.mark.parametrize("status_code", [500, "500"], ids=["valid", "invalid-type"])
def test_provider_failure_shapes_the_durable_and_visible_error(rust_session, monkeypatch, status_code):
    from agent.api_error_summary import ApiErrorSummaryMixin

    session, host, events = rust_session
    session.update(
        agent=SimpleNamespace(api_key="synthetic-key", provider="custom:loopback", model="example-model",
                              _summarize_api_error=ApiErrorSummaryMixin._summarize_api_error),
        _compute_host_turn_host=host, _compute_host_turn_id="current")
    monkeypatch.setattr(server, "_agent_status_update",
                        lambda sid, kind, text: server._emit("status.update", sid, {"kind": kind, "text": text}))
    host._handle_host_frame({"type": "turn.started", "sid": "sid", "request_id": "current"})
    host._receive_event({"method": "event", "params": {"session_id": "sid", "type": "message.complete",
        "payload": {"text": "provider unavailable", "status": "error"}}})

    settlement = host.terminal_details({"provider_error": {
        "status_code": status_code, "message": "temporary outage synthetic-key", "attempts": 1}})
    host.finish_events(persisted=True, receipt={"row_ids": [1], "complete": False})

    if isinstance(status_code, str):
        assert settlement == {"text": "provider unavailable", "status": "error"}
        assert [name for name, _ in events] == ["message.complete"]
        return
    assert [name for name, _ in events] == ["status.update", "message.complete"]
    assert settlement["failure_reason"] == events[1][1]["error_surface"]["code"] == "server_error"
    assert settlement["error"] == events[1][1]["error"]
    assert events[1][1]["recoverable"] is True
    assert "persisted_turn" not in events[1][1]
    assert "synthetic-key" not in str((settlement, events))


@pytest.mark.parametrize("outcome", ["crash", "close", "send_failed"])
def test_rust_dispatch_releases_turn_lease_on_every_terminal_path(rust_session, monkeypatch, outcome):
    from tui_gateway import rust_host_supervisor

    session, host, events = rust_session
    held = []
    sent = []

    @contextmanager
    def lease():
        held.append("held")
        try:
            yield
        finally:
            held.remove("held")

    def select(_sid, _session, _cfg, _frame, leases):
        leases.enter_context(lease())
        session["_rust_host"] = host
        return host

    def send(frame):
        if outcome == "send_failed":
            raise BrokenPipeError
        sent.append(frame)

    monkeypatch.setattr(rust_host_supervisor, "select_host", select)
    monkeypatch.setattr(host, "_send_frame", send)

    result = server._submit_prompt_to_compute_host("rpc", "sid", session, "hello")

    if outcome == "send_failed":
        assert result["error"]["code"] == 5019
        assert session["running"] and held == [] and events == []
        return
    assert result["result"]["status"] == "streaming" and held == ["held"]
    if outcome == "close":
        released = []
        session["active_session_lease"] = SimpleNamespace(
            lease_id="synthetic-active-lease", release=lambda: released.append(tuple(held)))
        monkeypatch.setattr(server, "_finalize_session", lambda *_a, **_kw: None)
        server._teardown_session(session)
        # The active slot becomes reusable only after the durable turn lease releases.
        assert released == [()] and not server._own_live_lease_ids()
        assert events == []
    else:
        host._fail_pending_turns(reason="crash", message="synthetic child exited")
        assert [payload["status"] for name, payload in events if name == "message.complete"] == ["error"]
    assert len(sent) == 1 and not session["running"] and held == []
    assert not server._session_uses_compute_host(session)


def test_crash_snapshot_hides_unowned_inventory_without_retiring_the_planner(rust_session, monkeypatch):
    from tui_gateway import rust_host_supervisor

    session, host, events = rust_session
    # Use the real shared session.info projection for both snapshots.
    monkeypatch.setattr(server, "_compute_host_session_info", lambda state: server._session_info(state["agent"], state))
    monkeypatch.setattr(rust_host_supervisor, "select_host", lambda *_: host)
    monkeypatch.setattr(host, "_send_frame", lambda _: None)
    planner = SimpleNamespace(model="example-model", tools=[{"function": {"name": "read_file"}}])
    session.update(agent=planner, _rust_host=host)
    with server._session_profile_runtime_scope(session, hydrate_secrets=False):
        assert server._session_info(planner, session)["tools"] == {"file": ["read_file"]}
        assert server._submit_prompt_to_compute_host("rpc", "sid", session, "hello")["result"]["turn_isolation"]
        host._fail_pending_turns(reason="crash", message="synthetic exit")
        normal = server._session_info(planner, session)

    crash = next(payload for name, payload in events if name == "session.info")
    assert crash["tools"] == crash["skills"] == {}
    assert crash["model"] == "example-model" and crash["running"] is False
    assert session["agent"] is planner and normal["tools"] == {"file": ["read_file"]}
    assert not server._session_uses_compute_host(session)


def test_disconnected_client_does_not_block_crash_settlement_or_respawn(tmp_path, monkeypatch):
    effects = []

    def disconnected(message):
        EVENTS["error"].payload.model_validate(message["params"]["payload"])
        effects.append("reported")
        raise OSError("disconnected")

    host = HostSupervisor(registry_path=tmp_path / "host.json", rpc_sink=disconnected, autostart=False)
    monkeypatch.setattr(host, "start", lambda: None)
    monkeypatch.setattr(host, "_send_frame", lambda _: None)
    monkeypatch.setattr(host, "_maybe_respawn_after_crash", lambda: effects.append("respawn"))
    host.submit_turn({"sid": "sid"}, on_complete=lambda frame: effects.append(frame["reason"]))
    process = host._proc = SimpleNamespace(wait=lambda: -9)

    host._wait_for_exit(process)

    assert effects == ["reported", "crash", "respawn"]


def test_stop_during_rust_preparation_preserves_the_admitted_slot(rust_session, monkeypatch):
    session, _host, _events = rust_session
    session.update(agent=SimpleNamespace(), _rust_host_preparing=True, _compute_host_turn_id="preparing")

    def unexpected_python_child(*_):
        pytest.fail("Stop should not start a Python child during Rust preparation")

    monkeypatch.setattr(server, "_get_compute_host_supervisor", unexpected_python_child)

    assert server._interrupt_session_turn("sid", session)

    assert session["_turn_cancel_requested"] and session["running"]


def test_queued_dispatch_failure_falls_back_inside_session_profile(rust_session, monkeypatch):
    session, _host, _events = rust_session
    session.update(running=False, queued_prompt={"text": "queued", "image_paths": ["queued.png"]})
    observed = []
    monkeypatch.setattr(server, "_submit_prompt_to_compute_host", lambda *_a, **_kw: {"error": {"code": 5019}})
    monkeypatch.setattr(server, "_start_agent_build", lambda *_: None)
    monkeypatch.setattr(server, "_wait_agent_for_prompt", lambda *_: None)
    monkeypatch.setattr(server, "_run_prompt_submit",
                        lambda _rid, _sid, _session, text, **kw: observed.append(
                            (str(get_hermes_home()), text, kw["image_paths"])))

    assert server._drain_queued_prompt("rpc", "sid", session)

    assert observed == [(session["profile_home"], "queued", ["queued.png"])]
