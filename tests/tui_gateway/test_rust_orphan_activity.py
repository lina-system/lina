"""A disconnected WebSocket must leave a progressing Rust turn running."""

from __future__ import annotations

import json
import logging
import os
import shutil
import time
from pathlib import Path
from queue import Queue
from threading import Event

import pytest
from starlette.applications import Starlette
from starlette.routing import WebSocketRoute
from starlette.testclient import TestClient, WebSocketTestSession
from starlette.websockets import WebSocket

from hermes_constants import get_hermes_home
from tests._fixtures.memory_store import PROCESS_STORE
from tests.tui_gateway._turn_host_parity_support import (
    MODEL,
    MODEL_KEY,
    ROOT,
    Json,
    ModelHandler,
    Reply,
    loopback_model,
)
from tests.waiting import wait_until
from tui_gateway import server
from tui_gateway.rust_host_supervisor import RustHostSupervisor
from tui_gateway.ws import handle_ws

pytestmark = pytest.mark.platforms("linux")
_log = logging.getLogger(__name__)


def _rpc(socket: WebSocketTestSession, method: str, **params: str) -> dict:
    socket.send_json({"jsonrpc": "2.0", "id": method, "method": method, "params": params})
    while (frame := socket.receive_json()).get("id") != method:
        assert frame.get("method") != "compute_host.activity"
    assert "error" not in frame, frame
    return frame["result"]


def test_detached_rust_turn_keeps_running_past_orphan_grace(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    binary = Path(shutil.which("lina-turn-host") or ROOT / "target/debug/lina-turn-host").resolve()
    assert binary.is_file(), f"Build lina-turn-host first or put it on PATH: {binary}"
    monkeypatch.setenv("PATH", os.pathsep.join((str(binary.parent), os.environ["PATH"])))
    monkeypatch.setenv("HERMES_TUI_TOOLSETS", "file_read")
    monkeypatch.setenv("LINA_STORE_DSN", PROCESS_STORE.app_dsn())
    monkeypatch.setattr(server, "_WS_ORPHAN_REAP_GRACE_S", 3.0)
    monkeypatch.setattr(server, "_WS_ORPHAN_ACTIVITY_STALE_S", 12.0)
    home = Path(get_hermes_home())
    monkeypatch.setattr(server, "_hermes_home", str(home))
    after_disconnect, finish_stream, disconnected = Event(), Event(), Event()
    frames: Queue[dict] = Queue()

    class StreamingHandler(ModelHandler):
        def _complete(self, request: Json, index: int, reply: Reply) -> None:
            assert request.get("stream") is True
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for text, gate in (
                ("First model chunk. " * 8, after_disconnect),
                ("Model chunk after disconnect. " * 8, finish_stream),
            ):
                chunk = {"choices": [{"index": 0, "delta": {"content": text}, "finish_reason": None}]}
                self.wfile.write(f"data: {json.dumps(chunk)}\n\n".encode())
                self.wfile.flush()
                assert gate.wait(20), "The WebSocket test did not release the model stream"
            self.wfile.write(b'data: {"choices":[{"index":0,"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')

    original = RustHostSupervisor._handle_host_frame

    def observe_host_frame(host: RustHostSupervisor, frame: dict) -> None:
        original(host, frame)
        if frame["type"] in {"turn.end", "turn.error"}:
            frames.put(frame)
        if frame["type"] == "rpc" and frame["message"]["method"] == "compute_host.activity":
            frames.put(frame)

    def wait_for_activity(since: int = 0) -> dict:
        deadline = time.monotonic() + 15
        while True:
            frame = frames.get(timeout=max(0, deadline - time.monotonic()))
            assert frame["type"] == "rpc", f"Rust turn settled before activity: {frame.get('reason')}"
            sample = frame["message"]["params"]
            if isinstance(stamp := sample["activity_ns"], int) and stamp >= since:
                return sample

    async def endpoint(socket: WebSocket) -> None:
        await handle_ws(socket)
        disconnected.set()

    monkeypatch.setattr(RustHostSupervisor, "_handle_host_frame", observe_host_frame)
    app = Starlette(routes=[WebSocketRoute("/ws", endpoint)])
    with loopback_model((Reply(),)) as model:
        model.RequestHandlerClass = StreamingHandler
        (home / "config.yaml").write_text(json.dumps({
            "dashboard": {"turn_isolation": True, "compute_host_impl": "rust"},
            "model": {"provider": "custom:activity", "default": MODEL, "context_length": 131072},
            "custom_providers": [{"name": "activity", "base_url": model.url, "api_key": MODEL_KEY}],
            "terminal": {"backend": "local", "cwd": str(tmp_path)},
            "agent": {"api_max_retries": 1, "auto_recovery_cycles": 0},
            "memory": {"memory_enabled": False, "user_profile_enabled": False},
            "auxiliary": {"title_generation": {"enabled": False}},
        }), encoding="utf-8")
        sid = ""
        try:
            with TestClient(app).websocket_connect("/ws") as socket:
                sid = _rpc(socket, "session.create", cwd=str(tmp_path), source="desktop")["session_id"]
                result = _rpc(socket, "prompt.submit", session_id=sid, text="Stream a response.")
                assert result.get("turn_isolation") is True, result
                before = wait_for_activity()
                session = server._sessions[sid]
                pid = session["_rust_host"].pid
                assert Path(f"/proc/{pid}/exe").resolve(strict=True) == binary
                assert before["session_id"] == sid
                assert before["turn_id"] == session["_compute_host_turn_id"]
                _log.info("Rust child pid=%s executable=%s emitted correlated activity before WebSocket close", pid, binary)
                socket.close()
                assert disconnected.wait(10), "WebSocket disconnect did not finish"

            assert server._ws_session_is_detached(session)
            timer = server._pending_ws_reaps[sid]
            detached_at = time.perf_counter_ns()
            after_disconnect.set()
            sample = wait_for_activity(since=detached_at)
            assert (sample["session_id"], sample["turn_id"]) == (sid, before["turn_id"])
            _log.info("Rust child emitted newer activity for the same turn after WebSocket disconnect")

            timer.join(timeout=10)
            assert not timer.is_alive(), "The original orphan grace timer did not fire"
            interrupt_requested = bool(session.get("_client_gone_interrupt_requested"))
            _log.info("Original %.1fs orphan grace expired, interrupt requested=%s",
                      timer.interval, interrupt_requested)
            assert not interrupt_requested, "Orphan deadline interrupted a detached turn with fresh Rust activity"
            assert server._sessions[sid] is session and session["running"]
            assert server._pending_ws_reaps[sid] is not timer

            finish_stream.set()
            while (terminal := frames.get(timeout=10))["type"] == "rpc":
                pass
            assert terminal["type"] == "turn.end" and terminal["interrupted"] is False
            wait_until(lambda: not session["running"], what="detached Rust turn settlement")
            _log.info("Detached Rust turn completed normally after the original orphan deadline")
        finally:
            after_disconnect.set()
            finish_stream.set()
            if sid:
                server._cancel_ws_orphan_reap(sid)
                server._shutdown_sessions()
