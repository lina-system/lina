"""Exercise the gateway supervisor's pressure checkpoint over the real native pipe."""

from __future__ import annotations

from contextlib import ExitStack
from queue import Queue
from threading import Lock
from types import SimpleNamespace

import pytest

from agent.agent_init import _init_usage_state
from agent.context_compressor import ContextCompressor
from tests.tui_gateway._turn_host_parity_support import MODEL, MODEL_KEY, Reply, loopback_model
from tests.tui_gateway.test_rust_host_rollback import rust_binary, rust_binary_on_path
from tui_gateway import server
from tui_gateway.rust_host_supervisor import RustHostSupervisor, select_host

__all__ = ["rust_binary", "rust_binary_on_path"]


@pytest.mark.platforms("linux")
@pytest.mark.parametrize(("threshold", "failure", "broken_preparation"), [
    (25, "context_pressure", False), (5000, None, False),
    (5000, "context_preflight_unavailable", False), (5000, "context_preflight_unavailable", True),
])
@pytest.mark.usefixtures("real_build_revision", "rust_binary_on_path")
def test_gateway_supervisor_enforces_pressure_before_native_continuation(
    tmp_path, threshold, failure, broken_preparation, monkeypatch, caplog,
):
    compressor = ContextCompressor(model=MODEL, config_context_length=100000)
    compressor.threshold_tokens = threshold
    if failure == "context_preflight_unavailable":
        def broken_pressure_check(_tokens):
            raise RuntimeError("private provider diagnostic")

        if broken_preparation:
            monkeypatch.setattr(RustHostSupervisor, "_prepare_context_preflight", staticmethod(broken_pressure_check))
        else:
            monkeypatch.setattr(compressor, "should_compress", broken_pressure_check)
    agent = SimpleNamespace(
        model=MODEL, provider="custom", api_mode="chat_completions", tools=[],
        context_compressor=compressor, compression_enabled=True,
        quiet_mode=True, verbose_logging=False, skip_context_files=True,
        max_compression_attempts=3, _session_db=None,
    )
    _init_usage_state(agent)
    user = {"role": "user", "content": "Read the example file."}
    stage = {
        "turn_id": "pressure-turn", "context_system": "Synthetic system", "wire_history": [],
        "rows": [user], "api_text": user["content"],
    }
    session = {
        "history_lock": Lock(), "running": True, "profile_home": str(tmp_path), "agent": agent,
        "_compute_host_turn_id": stage["turn_id"], "_rust_turn_stage": stage,
    }
    call = {
        "id": "read-1", "type": "function", "function": {"name": "read_file", "arguments": "[]"},
    }
    replies = (Reply(calls=(call,)),) if failure else (Reply(calls=(call,)), Reply(text="Finished."))
    terminal = Queue()
    cfg = {"compute_host_heartbeat_secs": 15, "compute_host_respawn_max": 1}
    with loopback_model(replies) as model:
        agent.base_url, agent.api_key = model.url, MODEL_KEY
        host = RustHostSupervisor(
            "pressure", session, home=str(tmp_path), credentials={"OPENAI_API_KEY": MODEL_KEY},
            endpoint=model.url, cfg=cfg,
        )
        session["_compute_host_turn_host"] = host
        try:
            host.submit_turn({
                "sid": "pressure", "turn_id": stage["turn_id"], "request_id": stage["turn_id"],
                "text": user["content"], "cwd": str(tmp_path), "terminal_backend": "local",
                "turn_plan": {
                    "system": stage["context_system"], "tools": [], "model": MODEL,
                    "base_url": model.url, "params": {"timeout": 5}, "stream": False,
                },
            }, on_complete=terminal.put)
            end = terminal.get(timeout=12)
            details = host.terminal_details(end)
        finally:
            host.shutdown()

    assert end["type"] == "turn.end"
    assert end["new_messages"][2]["tool_call_id"] == "read-1"
    assert "read_file arguments must be a JSON object" in end["new_messages"][2]["content"]
    assert model.errors == []
    assert agent.session_api_calls == (0 if broken_preparation else 1)
    if failure:
        assert len(model.requests) == 1
        assert details["status"] == "error"
        assert details["failure_reason"] == failure
        assert details["failure_retryable"] is True
        if failure == "context_preflight_unavailable":
            assert "context preflight failed" in caplog.text
            assert "private provider diagnostic" not in caplog.text
    else:
        assert len(model.requests) == 2
        assert details["status"] == "complete"
        assert model.requests[1]["messages"][-1]["tool_call_id"] == "read-1"


def test_python_selector_ignores_native_recovery_on_minimal_session(monkeypatch):
    sentinel = object()
    monkeypatch.setattr(server, "_get_compute_host_supervisor", lambda _cfg: sentinel)
    session = {"_rust_context_recovery_pending": True}
    with ExitStack() as leases:
        assert select_host("example", session, {"compute_host_impl": "python"}, {}, leases) is sentinel
    assert session["_rust_context_recovery_pending"] is True
