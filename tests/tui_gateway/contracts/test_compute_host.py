"""Exercise the compute-host contract against JSON lines from the current producer."""

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

import pytest
from pydantic import TypeAdapter, ValidationError

from scripts.gen_gateway_contracts import COMPUTE_HOST_OUT, render_compute_host
from tests.tui_gateway._compute_host_frames import FrameSink
from tests.tui_gateway.test_compute_host_turn_protocol import (
    _agent,
    _session,
    turn_env,
)
from tui_gateway import server
from tui_gateway.compute_host import ComputeHost
from tui_gateway.contracts.compute_host import ComputeHostRequest, ComputeHostResponse, TurnPlan

__all__ = ["turn_env"]

REQUEST = TypeAdapter(ComputeHostRequest)
RESPONSE = TypeAdapter(ComputeHostResponse)
REPO = Path(__file__).resolve().parents[3]


def _assert_response_roundtrip(frames: list[dict]) -> None:
    for frame in frames:
        parsed = RESPONSE.validate_python(frame)
        assert RESPONSE.dump_python(parsed, mode="json", exclude_unset=True) == frame


def test_compute_host_pipe_frames_roundtrip() -> None:
    # Include malformed input so the generic error without a request_id travels the real pipe.
    frames = [
        {"type": "interrupt", "sid": "absent", "request_id": "interrupt"},
        {"type": "respond", "sid": "absent", "request_id": "respond", "params": {}},
        {"type": "control", "sid": "absent", "request_id": "control", "route_name": "session.save"},
        {"type": "shutdown", "request_id": "shutdown"},
    ]
    lines = ["{", '{"type":"bogus","request_id":"unknown"}']
    for frame in frames:
        parsed = REQUEST.validate_python(frame)
        assert REQUEST.dump_python(parsed, mode="json", exclude_unset=True) == frame
        lines.append(json.dumps(frame))
    proc = subprocess.run(
        [sys.executable, "-B", "-m", "tui_gateway.compute_host"],
        cwd=REPO,
        env={**os.environ, "HERMES_COMPUTE_HOST_HEARTBEAT_SECS": "0"},
        input="\n".join(lines) + "\n",
        capture_output=True,
        text=True,
        timeout=30,
        check=True,
    )
    replies = [json.loads(line) for line in proc.stdout.splitlines()]
    assert [(frame["type"], frame.get("request_id")) for frame in replies] == [
        ("hello", None), ("error", None), ("error", "unknown"), ("interrupt.ack", "interrupt"),
        ("respond.error", "respond"), ("control.error", "control"), ("shutdown.ack", "shutdown"),
    ]
    _assert_response_roundtrip(replies)


def test_compute_host_turn_frames_roundtrip(turn_env, monkeypatch) -> None:
    out = FrameSink()
    host = ComputeHost(stdout=out, heartbeat_secs=0)
    sid = "contract-turn"
    session = _session(_agent(["one ", "two"]))
    monkeypatch.setitem(server._sessions, sid, session)
    frame = server._compute_host_turn_frame("complete", sid, session, "hello")
    try:
        parsed = REQUEST.validate_python(frame)
        assert REQUEST.dump_python(parsed, mode="json", exclude_unset=True) == frame
        host.handle_frame(frame)
        end = out.wait_for(lambda f: f.get("request_id") == "complete" and f["type"] != "turn.started")
        assert end["type"] == "turn.end"
        assert end["message_count"] == 2
        assert any(
            f["type"] == "rpc" and f["message"].get("params", {}).get("type") == "message.delta"
            for f in out.frames()
        )
        host.handle_frame({**frame, "request_id": "stale", "queued_prompt_generation": -1})
        stale = out.wait_for(lambda f: f.get("request_id") == "stale")
        assert stale["type"] == "turn.end" and stale["interrupted"] and "session_info" not in stale
        host.handle_frame({**frame, "request_id": "missing-sid", "sid": ""})
        error = out.wait_for(lambda f: f.get("request_id") == "missing-sid")
        assert error["type"] == "turn.error"
        _assert_response_roundtrip(out.frames())
    finally:
        host.close()


def test_compute_host_control_payloads_roundtrip(monkeypatch) -> None:
    out = FrameSink()
    host = ComputeHost(stdout=out, heartbeat_secs=0)
    sid = "contract-control"
    session = _session(_agent([]))
    session["history"] = [{"role": "user", "content": "hello"}]
    monkeypatch.setitem(server._sessions, sid, session)
    monkeypatch.setattr(
        server, "handle_request",
        lambda frame: {"jsonrpc": "2.0", "id": frame["id"], "result": {"status": "ok"}},
    )
    requests = [
        {"type": "respond", "sid": sid, "request_id": "cancel",
         "params": {"cancel": {"method": "clarify", "reason": "resolved"}}},
        {"type": "control", "sid": sid, "request_id": "slash", "route_name": "slash.prompt"},
        {"type": "reload_mcp", "sid": sid, "request_id": "reload", "route_name": "reload.mcp"},
    ]
    # Drive the real response relay and slash transcript projector without model calls.
    try:
        for frame in requests:
            parsed = REQUEST.validate_python(frame)
            assert REQUEST.dump_python(parsed, mode="json", exclude_unset=True) == frame
            host.handle_frame(frame)
        replies = out.frames()
        assert [frame["type"] for frame in replies] == ["respond.ack", "control.ack", "reload_mcp.ack"]
        assert replies[1]["messages"] == [{"role": "user", "text": "hello"}]
        _assert_response_roundtrip(replies)
    finally:
        host.close()


def test_compute_host_heartbeat_roundtrips() -> None:
    out = FrameSink()
    host = ComputeHost(stdout=out, heartbeat_secs=0.01)
    try:
        _assert_response_roundtrip([out.wait_for(lambda frame: frame["type"] == "hb")])
    finally:
        host.close()


def _plan() -> dict:
    return {
        "system": "Answer briefly.",
        "tools": [{"type": "function", "function": {"name": "lookup", "parameters": {"type": "object"}}}],
        "model": "example-model",
        "base_url": "https://example.invalid/v1?api-version=1",
        "params": {"temperature": 0.2, "extra_body": {"reasoning": {"enabled": False}}},
        "stream": True,
    }


def test_planned_turn_roundtrips_provider_options_and_messages() -> None:
    frame = {
        "type": "turn.start",
        "sid": "planned",
        "terminal_backend": "local",
        "turn_plan": _plan(),
        "messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
    }
    parsed = REQUEST.validate_python(frame)
    assert REQUEST.dump_python(parsed, mode="json", exclude_unset=True) == frame


def test_rust_turn_completion_preserves_history_and_suffix() -> None:
    old_message = {"role": "assistant", "content": "Earlier answer"}
    suffix = [{"role": "user", "content": "New question"}, {"role": "assistant", "content": "New answer"}]
    _assert_response_roundtrip([{
        "type": "turn.end", "sid": "planned", "request_id": "request", "turn_id": "turn",
        "host_ns": 200, "ended_ns": 199, "interrupted": False,
        "messages": [old_message, *suffix], "new_messages": suffix,
    }])


def test_http_failure_summary_roundtrips_without_raw_provider_fields() -> None:
    frame = {
        "type": "turn.end", "sid": "planned", "request_id": "request", "host_ns": 200,
        "ended_ns": 199, "interrupted": False,
    }
    summary = {"status_code": 503, "message": "Service unavailable", "attempts": 1}
    _assert_response_roundtrip([{**frame, "provider_error": summary}])
    for invalid in ({"body": "raw"}, {"attempts": 0}, {"status_code": 200},
                    {"message": "x" * 513}, {"message": "line\nbreak"}):
        with pytest.raises(ValidationError):
            RESPONSE.validate_python({**frame, "provider_error": {**summary, **invalid}})


def test_read_file_policy_roundtrips_real_authorize_and_redact_replies(tmp_path: Path) -> None:
    from tui_gateway.rust_file_policy import handle_file_policy

    synthetic_token = "ghp_" + "SyntheticFixture" + "01234567890123456789"
    content = f"token: {synthetic_token}\n"
    path = tmp_path / "service.txt"
    path.write_text(content, encoding="utf-8")
    session = {"cwd": str(tmp_path)}
    authorize = {
        "type": "read_file.policy", "sid": "session", "turn_id": "turn", "request_id": "authorize",
        "op": "authorize", "path": path.name,
    }
    authorization = handle_file_policy(session, authorize)
    assert authorization["ok"] is True
    redact = {
        **authorize, "request_id": "redact", "op": "redact", "content": content,
        "resolved": authorization["resolved"], "identity": authorization["identity"],
    }
    redaction = handle_file_policy(session, redact)
    assert redaction["ok"] is True
    assert synthetic_token not in redaction["content"]

    missing = {**authorize, "request_id": "missing", "path": "absent.txt"}
    blocked = {**authorize, "request_id": "blocked", "path": ".env"}
    missing_reply = handle_file_policy(session, missing)
    denied_reply = handle_file_policy(session, blocked)
    assert missing_reply["ok"] is False and missing_reply["result"]["not_found"] is True
    assert denied_reply["ok"] is False and "error" in denied_reply
    _assert_response_roundtrip([authorize, redact, missing, blocked])
    for frame in (authorization, redaction, missing_reply, denied_reply):
        parsed = REQUEST.validate_python(frame)
        assert REQUEST.dump_python(parsed, mode="json", exclude_unset=True) == frame


def test_read_file_policy_requires_authorization_identity_on_redact() -> None:
    with pytest.raises(ValidationError) as caught:
        RESPONSE.validate_python({
            "type": "read_file.policy", "sid": "session", "turn_id": "turn", "request_id": "redact",
            "op": "redact", "path": "service.txt", "resolved": "/workspace/service.txt",
            "content": "synthetic content",
        })
    assert [error["loc"][-1] for error in caught.value.errors(include_input=False)] == ["identity"]


@pytest.mark.parametrize(
    ("override", "error_type"),
    [
        ({"api_key": "placeholder"}, "extra_forbidden"),
        ({"params": {"api_key": "placeholder"}}, "turn_plan_credentials"),
        ({"params": {"extra_body": {"nested": [{"Authorization": "placeholder"}]}}}, "turn_plan_credentials"),
        ({"base_url": "https://placeholder@example.invalid/v1"}, "turn_plan_credentials"),
        ({"base_url": "https://example.invalid/v1?api%2Dkey=placeholder"}, "turn_plan_credentials"),
    ],
)
def test_turn_plan_rejects_credentials(override, error_type) -> None:
    with pytest.raises(ValidationError) as caught:
        TurnPlan.model_validate({**_plan(), **override})
    assert [error["type"] for error in caught.value.errors(include_input=False)] == [error_type]


def test_compute_host_schema_is_current() -> None:
    assert COMPUTE_HOST_OUT.read_bytes() == render_compute_host().encode("utf-8")
