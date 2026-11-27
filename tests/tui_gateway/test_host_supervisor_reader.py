"""Exercise compute host frame recovery over a subprocess's stdio pipes."""

import errno
import queue
import re
import sys
from threading import RLock

import pytest

from tests.waiting import wait_until
from tui_gateway.host_supervisor import HostSupervisor
from tui_gateway.rust_host_supervisor import RustHostSupervisor


_CHILD = """
import json
import sys
from threading import Event

def emit(kind, **fields):
    print(json.dumps({"type": kind, **fields}), flush=True)

def event(sid, text, **identity):
    emit("rpc", sid=sid, **identity, message={"jsonrpc": "2.0", "method": "event", "params": {
        "session_id": sid, "type": "message.delta", "payload": {"text": text}
    }})

emit("hello", build_sha=sys.argv[2] if len(sys.argv) > 2 else "unknown")
mode = sys.argv[1]
interrupted = False
other_finished = False
for line in sys.stdin:
    frame = json.loads(line)
    kind, request_id = frame["type"], frame["request_id"]
    sid = frame.get("sid")
    if kind == "shutdown":
        break
    if kind == "interrupt":
        interrupted = sid == "first" and frame.get("turn_id") == "failed"
        if mode == "slow-settlement" and interrupted:
            # Model a writer still draining after the supervisor's interrupt deadline.
            Event().wait(0.5)
            emit("turn.end", sid="second", request_id="other")
            other_finished = True
        if mode == "wait-for-policy":
            emit("interrupt.ack", sid=sid, request_id=request_id, applied=interrupted)
            emit("hb", progress_counter=73)
            continue
        event("first", "discard after fault")
    if kind == "turn.start":
        emit("turn.started", sid=sid, request_id=request_id)
        if request_id == "failed":
            if mode == "wait-for-policy":
                emit("read_file.policy", sid=sid, request_id="policy", turn_id=request_id,
                     op="redact", content="synthetic private file")
            else:
                event(sid, "synthetic private frame")
        elif request_id == "other":
            if mode == "slow-settlement":
                continue
            event(sid, "unrelated progress")
            emit("hb", progress_counter=42)
            emit("turn.end", sid=sid, request_id=request_id)
            other_finished = True
        elif request_id == "next":
            event(sid, "discard old identity", turn_id="failed")
            event(sid, "fresh progress", turn_id=request_id)
            emit("turn.end", sid=sid, request_id=request_id)
    if interrupted and other_finished:
        emit("turn.end", sid="first", request_id="failed", history_version=7, interrupted=True)
        interrupted = False
"""


def test_slow_interrupted_turn_preserves_other_session(tmp_path, monkeypatch):
    completed = queue.Queue()

    def receive(message):
        if message["params"]["payload"].get("text") == "synthetic private frame":
            raise ValueError("synthetic private exception")

    monkeypatch.setattr("tui_gateway.host_supervisor._FRAME_INTERRUPT_GRACE_SECS", 0)
    host = HostSupervisor(
        argv=[sys.executable, "-u", "-c", _CHILD, "slow-settlement"],
        registry_path=tmp_path / "host.json", expected_build_sha="unknown", rpc_sink=receive,
    )
    try:
        pid = host.pid
        host.submit_turn({"sid": "second", "request_id": "other"}, on_complete=completed.put)
        host.submit_turn({"sid": "first", "request_id": "failed"}, on_complete=completed.put)
        other = completed.get(timeout=5)
        assert (other["request_id"], other["type"]) == ("other", "turn.end")
        failed = completed.get(timeout=5)
        assert (failed["request_id"], failed["reason"], failed["history_version"]) == (
            "failed", "frame_handler_failed", 7)
        assert host.is_running() and host.pid == pid
    finally:
        host.shutdown()


def test_submit_during_exit_is_retryable(tmp_path):
    completed = queue.Queue()
    host = HostSupervisor(
        argv=[sys.executable, "-u", "-c", _CHILD, "normal"],
        registry_path=tmp_path / "host.json", expected_build_sha="unknown",
    )
    frame = {"sid": "second", "request_id": "other"}
    try:
        with host._lock:  # Hold the exit watcher at the boundary between wait() and cleanup.
            proc = host._proc
            proc.terminate()
            proc.wait(timeout=5)
            with pytest.raises(BlockingIOError) as raised:
                host.submit_turn(frame, on_complete=completed.put)
            assert raised.value.errno == errno.EAGAIN
        assert wait_until(lambda: host.is_running() and host.pid != proc.pid, timeout=5)
        host.submit_turn(frame, on_complete=completed.put)
        terminal = completed.get(timeout=5)
        assert (terminal["request_id"], terminal["type"]) == ("other", "turn.end")
        assert completed.empty()
    finally:
        host.shutdown()


@pytest.mark.parametrize("boundary", ["reader", "completion"])
def test_handler_log_locates_fault_without_payload(tmp_path, caplog, boundary):
    def reject_payload(frame):
        raise ValueError("private exception marker", frame)

    host = HostSupervisor(
        argv=[sys.executable, "-u", "-c", _CHILD, "normal"],
        registry_path=tmp_path / "host.json", expected_build_sha="unknown",
        rpc_sink=reject_payload if boundary == "reader" else None,
    )
    try:
        host.submit_turn({"sid": "private-session-marker", "request_id": "other"},
                         on_complete=reject_payload if boundary == "completion" else None)
        assert wait_until(lambda: "ValueError" in caplog.text, timeout=5)
        assert re.search(r"test_host_supervisor_reader\.py:\d+ in reject_payload", caplog.text)
        for value in ("private-session-marker", "private exception marker", "unrelated progress"):
            assert value not in caplog.text
    finally:
        host.shutdown()


def test_reader_recovers_correlated_turn_after_rpc_handler_fault(tmp_path, caplog):
    completed: queue.Queue[dict] = queue.Queue()
    delivered = []

    def receive(message: dict) -> None:
        text = message["params"]["payload"].get("text")
        if text == "synthetic private frame":
            raise RuntimeError("synthetic private exception")
        if text is not None:
            delivered.append(text)

    host = HostSupervisor(
        argv=[sys.executable, "-u", "-c", _CHILD, "normal"], registry_path=tmp_path / "host.json",
        expected_build_sha="unknown", rpc_sink=receive, respawn_max=0,
    )
    try:
        pid = host.pid
        host.submit_turn({"sid": "first", "request_id": "failed"}, on_complete=completed.put)
        host.submit_turn({"sid": "second", "request_id": "other"}, on_complete=completed.put)
        try:
            other = completed.get(timeout=5)
        except queue.Empty as exc:
            raise AssertionError("stdout reader stopped before delivering the following turn") from exc
        assert (other["request_id"], other["type"]) == ("other", "turn.end")
        assert host._last_progress_counter == 42
        failed = completed.get(timeout=5)
        assert (failed["request_id"], failed["type"]) == ("failed", "turn.error")
        assert failed["history_version"] == 7
        assert not host.has_pending_turn("first")

        host.submit_turn({"sid": "first", "request_id": "next"}, on_complete=completed.put)
        next_turn = completed.get(timeout=5)
        assert (next_turn["request_id"], next_turn["type"]) == ("next", "turn.end")
        assert host.is_running() and host.pid == pid
        assert completed.empty()
        assert delivered == ["unrelated progress", "fresh progress"]
        assert "RuntimeError" in caplog.text
        assert "synthetic private frame" not in caplog.text
        assert "synthetic private exception" not in caplog.text
    finally:
        host.shutdown()


@pytest.mark.parametrize("write_fails", [False, True], ids=["policy-wait", "interrupt-write"])
def test_reader_reaps_child_when_failed_turn_cannot_interrupt(tmp_path, caplog, monkeypatch, write_fails):
    completed: queue.Queue[dict] = queue.Queue()
    # A read policy request belongs to the dedicated native host for this session.
    session = {"history_lock": RLock(), "_compute_host_turn_id": "failed"}
    host = RustHostSupervisor(
        "first", session, home=str(tmp_path), credentials={"OPENAI_API_KEY": ""}, endpoint="",
        cfg={"compute_host_heartbeat_secs": 15, "compute_host_respawn_max": 0},
    )
    session["_compute_host_turn_host"] = host
    host.expected_build_sha = "a" * 40
    host.argv = [sys.executable, "-u", "-c", _CHILD, "wait-for-policy", host.expected_build_sha]
    dispatch = host._handle_host_frame
    send = host._send_frame

    def fault_policy(frame: dict) -> None:
        if frame["type"] == "read_file.policy":
            raise LookupError(frame["content"])
        dispatch(frame)

    def send_with_failed_interrupt(frame: dict) -> None:
        if frame["type"] == "interrupt":
            raise BrokenPipeError("synthetic private write")
        send(frame)

    monkeypatch.setattr(host, "_handle_host_frame", fault_policy)
    if write_fails:
        monkeypatch.setattr(host, "_send_frame", send_with_failed_interrupt)
    try:
        host.submit_turn({"sid": "first", "request_id": "failed", "turn_id": "failed"}, on_complete=completed.put)
        try:
            failure = completed.get(timeout=15)
        except queue.Empty as exc:
            raise AssertionError("a failed turn remained pending when the child could not settle it") from exc
        assert (failure["request_id"], failure["type"], failure["reason"]) == ("failed", "turn.error", "crash")
        assert not host.is_running() and not host.has_pending_turn("first")
        assert "LookupError" in caplog.text
        assert "synthetic private file" not in caplog.text
        if write_fails:
            assert "BrokenPipeError" in caplog.text
            assert "synthetic private write" not in caplog.text
        else:
            assert host._last_progress_counter == 73
    finally:
        host.shutdown()
