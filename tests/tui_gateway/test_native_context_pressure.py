"""Exercise the gateway supervisor's pressure checkpoint over the real native pipe."""

from __future__ import annotations

import shutil
from contextlib import ExitStack
from pathlib import Path
from queue import Queue
from threading import Lock
from types import SimpleNamespace

import pytest

from agent.context_compressor import ContextCompressor
from tests.tui_gateway._turn_host_parity_support import MODEL, MODEL_KEY, ROOT, Reply, loopback_model
from tui_gateway import server
from tui_gateway.rust_host_supervisor import RustHostSupervisor, select_host


@pytest.mark.platforms("linux")
@pytest.mark.parametrize(("threshold", "failure"), [
    (25, "context_pressure"), (5000, None), (5000, "context_preflight_unavailable"),
])
@pytest.mark.usefixtures("real_build_revision")
def test_gateway_supervisor_enforces_pressure_before_native_continuation(tmp_path, threshold, failure, monkeypatch, caplog):
    binary = Path(shutil.which("lina-turn-host") or ROOT / "target/debug/lina-turn-host").resolve()
    assert binary.is_file(), f"Build lina-turn-host first or put it on PATH: {binary}"
    compressor = ContextCompressor(model=MODEL, config_context_length=100000)
    compressor.threshold_tokens = threshold
    if failure == "context_preflight_unavailable":
        def broken_pressure_check(_tokens):
            raise RuntimeError("private provider diagnostic")

        monkeypatch.setattr(compressor, "should_compress", broken_pressure_check)
    agent = SimpleNamespace(
        model=MODEL, provider="custom", api_mode="chat_completions", tools=[],
        context_compressor=compressor, compression_enabled=True,
    )
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
