"""Process and loopback fixtures for the real compute-host parity tests."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import threading
from collections import deque
from collections.abc import Callable, Iterator
from contextlib import ExitStack, closing, contextmanager
from dataclasses import dataclass
from fcntl import LOCK_EX, flock
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Final, TypeAlias
from unittest.mock import patch

from pydantic import JsonValue

from tests.tui_gateway._compute_host_frames import FrameSink

ROOT: Final = Path(__file__).resolve().parents[2]
SID: Final = "abcd1234"
SESSION_KEY: Final = "20260101_000000_abcd1234"
MODEL: Final = "parity-model"
MODEL_KEY: Final = "synthetic-loopback"
TIMEOUT: Final = 30
UID_PREFIX: Final = "504152495459400080000000"
Json: TypeAlias = dict[str, JsonValue]

# Startup code for the two real Python processes in an arm. flock protects the
# shared sequence across the parent, the Python compute host, and its respawn.
UID_STARTUP: Final = f"""
from fcntl import flock, LOCK_EX
from pathlib import Path
from uuid import UUID

from agent import message_metadata

counter = Path(__file__).with_name("uid-counter")

def next_uuid():
    with counter.open("r+") as state:
        flock(state, LOCK_EX)
        sequence = int(state.read()) + 1
        state.seek(0)
        state.write(str(sequence))
        state.truncate()
    return UUID(hex="{UID_PREFIX}" + format(sequence, "08x"))

message_metadata.uuid4 = next_uuid
"""


@dataclass(frozen=True, slots=True)
class Reply:
    text: str | None = None
    calls: tuple[Json, ...] = ()
    status: int = 200
    gate: threading.Event | None = None
    usage: Json | None = None


class ModelServer(ThreadingHTTPServer):
    """Record every POST before responding with the next scripted completion."""

    def __init__(self, replies: tuple[Reply, ...]) -> None:
        self.replies = replies
        self.requests: list[Json] = []
        self.errors: list[str] = []
        self.condition = threading.Condition()
        super().__init__(("127.0.0.1", 0), ModelHandler)

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.server_port}/v1"

    def wait_for_requests(self, count: int) -> None:
        with self.condition:
            assert self.condition.wait_for(lambda: len(self.requests) >= count, TIMEOUT)


class ModelHandler(BaseHTTPRequestHandler):
    server: ModelServer

    def log_message(self, _format: str, *args: str) -> None:
        pass

    def do_POST(self) -> None:
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        with self.server.condition:
            index = len(self.server.requests)
            self.server.requests.append(body)
            self.server.condition.notify_all()
        if self.path != "/v1/chat/completions" or index >= len(self.server.replies):
            self.server.errors.append(f"Unexpected model POST {index + 1}: {self.path}")
            self.send_error(400)
            return
        reply = self.server.replies[index]
        if reply.gate is not None and not reply.gate.wait(TIMEOUT):
            self.server.errors.append("The test did not release the model response")
            self.send_error(504)
            return
        if reply.status != 200:
            error = {"message": "parity model failure", "type": "server_error", "code": "parity_500"}
            self._send(reply.status, "application/json", json.dumps({"error": error}).encode())
            return
        try:
            self._complete(body, index, reply)
        except (BrokenPipeError, ConnectionResetError):
            if reply.gate is None:
                raise

    def _send(self, status: int, content_type: str, data: bytes) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _complete(self, request: Json, index: int, reply: Reply) -> None:
        finish = "tool_calls" if reply.calls else "stop"
        envelope = {"id": f"parity-{index}", "created": 1, "model": MODEL}
        message = {"role": "assistant", "content": reply.text}
        if reply.calls:
            message["tool_calls"] = list(reply.calls)
        usage = reply.usage if reply.usage is not None else {
            "prompt_tokens": 20, "completion_tokens": 7, "total_tokens": 27,
        }
        if request.get("stream"):
            envelope["object"] = "chat.completion.chunk"
            if reply.calls:
                message["tool_calls"] = [
                    {"index": idx, **call} for idx, call in enumerate(reply.calls)
                ]
            chunks = [
                {"choices": [{"index": 0, "delta": message, "finish_reason": None}]},
                {"choices": [{"index": 0, "delta": {}, "finish_reason": finish}]},
                {"choices": [], "usage": usage},
            ]
            data = "".join(f"data: {json.dumps({**envelope, **chunk})}\n\n" for chunk in chunks)
            self._send(200, "text/event-stream", (data + "data: [DONE]\n\n").encode())
        else:
            result = {**envelope, "object": "chat.completion", "usage": usage,
                      "choices": [{"index": 0, "message": message, "finish_reason": finish}]}
            self._send(200, "application/json", json.dumps(result).encode())


@contextmanager
def loopback_model(replies: tuple[Reply, ...]) -> Iterator[ModelServer]:
    with ModelServer(replies) as model:
        thread = threading.Thread(target=model.serve_forever, daemon=True)
        thread.start()
        try:
            yield model
        finally:
            for reply in replies:
                if reply.gate is not None:
                    reply.gate.set()
            model.shutdown()
            thread.join(TIMEOUT)


class GatewayClient:
    """Drive real parent JSON-RPC and observe its stdout and host lifecycle."""

    def __init__(self, process: subprocess.Popen[str], secrets: tuple[str, ...]) -> None:
        self.process = process
        self.secrets = secrets
        self.stderr: deque[str] = deque(maxlen=30)
        self.sink = FrameSink()
        self.next_id = 0
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.stderr_reader = threading.Thread(target=self._read_stderr, daemon=True)
        self.reader.start()
        self.stderr_reader.start()

    def _read(self) -> None:
        assert self.process.stdout is not None
        for line in self.process.stdout:
            if any(secret in line for secret in self.secrets):
                self.sink.write('{"parity_credential_leak":true}\n')
            else:
                self.sink.write(line)
        self.sink.write('{"parity_gateway_eof":true}\n')

    def _read_stderr(self) -> None:
        assert self.process.stderr is not None
        for line in self.process.stderr:
            for secret in self.secrets:
                line = line.replace(secret, "<redacted fixture credential>")
            self.stderr.append(line[:500].rstrip() + "\n")

    def wait(self, predicate: Callable[[Json], bool], *, after: int = 0) -> Json:
        def matches(frame: Json) -> bool:
            return bool(frame.get("parity_gateway_eof") or frame.get("parity_credential_leak")) or (
                predicate(frame) and any(candidate is frame for candidate in self.sink.frames()[after:])
            )

        try:
            frame = self.sink.wait_for(matches, TIMEOUT)
            assert not frame.get("parity_gateway_eof"), "Gateway exited before the expected frame"
            assert not frame.get("parity_credential_leak"), "Gateway emitted a fixture credential"
            return frame
        except AssertionError:
            seen = [
                frame["params"]["type"] if frame.get("method") == "event" else
                frame.get("parity_host_frame", {}).get("type", frame.get("id"))
                for frame in self.sink.frames()[after:]
            ]
            raise AssertionError(
                f"Expected gateway frame did not arrive; observed frame types or response IDs: {seen}\n"
                f"Gateway stderr:\n{''.join(self.stderr.copy())}"
            ) from None

    def rpc(self, method: str, **params: JsonValue) -> Json:
        assert self.process.stdin is not None
        self.next_id += 1
        rid = self.next_id
        request = {"jsonrpc": "2.0", "id": rid, "method": method, "params": params}
        self.process.stdin.write(json.dumps(request) + "\n")
        self.process.stdin.flush()
        result = self.wait(lambda frame: frame.get("id") == rid)
        assert "error" not in result, result
        return result["result"]


@contextmanager
def gateway(
    home: Path, binary: Path, extra_env: dict[str, str], store_dsn: str, secrets: tuple[str, ...],
) -> Iterator[GatewayClient]:
    startup = home / "parity-startup"
    startup.mkdir(exist_ok=True)
    with (startup / "uid-counter").open("a+", encoding="ascii") as counter:
        flock(counter, LOCK_EX)
        counter.seek(0)
        if not counter.read():
            counter.write("0")
            counter.flush()
        hook = startup / "sitecustomize.py"
        if not hook.exists():
            hook.write_text(UID_STARTUP, encoding="utf-8")
    # An allowlist keeps credentials, proxies, dotenv selectors and model defaults out.
    env = {
        "PATH": f"{binary.parent}{os.pathsep}{os.defpath}",
        "PYTHONPATH": os.pathsep.join((str(startup), str(ROOT))),
        "HOME": str(home),
        "HERMES_HOME": str(home),
        "XDG_CONFIG_HOME": str(home / "config"),
        "XDG_CACHE_HOME": str(home / "cache"),
        "XDG_DATA_HOME": str(home / "data"),
        "XDG_STATE_HOME": str(home / "state"),
        "TMPDIR": str(home),
        "TZ": os.environ.get("TZ", "UTC"),
        "LANG": "C.UTF-8",
        "PYTHONDONTWRITEBYTECODE": "1",
        "HERMES_TUI_TOOLSETS": "file_read",
        "LINA_STORE_DSN": store_dsn,
        **extra_env,
    }
    with subprocess.Popen(
        [sys.executable, "-m", "tests.tui_gateway._turn_host_parity_support"],
        cwd=ROOT, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, text=True, encoding="utf-8", start_new_session=True,
    ) as process:
        client = GatewayClient(process, secrets)
        try:
            yield client
        finally:
            assert process.stdin is not None
            process.stdin.close()
            try:
                process.wait(TIMEOUT)
            except subprocess.TimeoutExpired:
                import signal

                os.killpg(process.pid, signal.SIGKILL)
                process.wait(TIMEOUT)
                raise
            finally:
                client.reader.join(TIMEOUT)
                client.stderr_reader.join(TIMEOUT)
        assert process.returncode == 0, f"Gateway exited {process.returncode}\n{''.join(client.stderr)}"


def stored_rows(home: Path) -> list[Json]:
    """Read the durable rows independently of the gateway and the host frames."""
    from hermes_state import SessionDB

    with closing(SessionDB(home / "state.db", read_only=True)) as db:
        return db.get_messages(SESSION_KEY, include_inactive=True)


def _serve_parent() -> None:
    # Keep logs out of the wire and keep async notifications on the client channel.
    wire = sys.stdout

    from tui_gateway import server
    from tui_gateway.host_supervisor import HostSupervisor
    from tui_gateway.transport import StdioTransport

    sys.stdout = sys.stderr
    transport = StdioTransport(lambda: wire, server._stdout_lock)
    server._stdio_transport = transport
    server._stdio_is_rpc_channel = True
    server._live_transports.add(transport)
    supervisors: list[HostSupervisor] = []
    start = HostSupervisor.start
    handle_host_frame = HostSupervisor._handle_host_frame

    def observe_start(self: HostSupervisor) -> None:
        if self not in supervisors:
            supervisors.append(self)
        start(self)

    def observe_host_frame(self: HostSupervisor, frame: Json) -> None:
        handle_host_frame(self, frame)
        if frame["type"] in {"turn.end", "turn.error", "interrupt.ack"}:
            transport.write({"parity_host_frame": {
                key: frame[key] for key in ("type", "reason", "applied") if key in frame
            }})

    # Fixed fixture session IDs avoid normalizing session identity out of the observations.
    with ExitStack() as patches:
        patches.enter_context(patch.object(server, "_new_runtime_ids", return_value=(SID, "desktop")))
        patches.enter_context(patch.object(server, "_new_session_key", return_value=SESSION_KEY))
        patches.enter_context(patch.object(HostSupervisor, "start", observe_start))
        patches.enter_context(patch.object(HostSupervisor, "_handle_host_frame", observe_host_frame))
        if os.getenv("PARITY_READ_TARGET") and os.getenv("PARITY_RUST") == "1":
            from tui_gateway.rust_host_supervisor import RustHostSupervisor

            child_environment = RustHostSupervisor._child_environment
            gate_env = {name: os.environ[name] for name in (
                "LD_PRELOAD", "PARITY_READ_TARGET", "PARITY_READ_ENTERED", "PARITY_READ_RELEASE",
            )}

            def include_read_gate(self: RustHostSupervisor) -> dict[str, str]:
                return {**child_environment(self), **gate_env}

            patches.enter_context(
                patch.object(RustHostSupervisor, "_child_environment", include_read_gate),
            )
        try:
            for line in sys.stdin:
                response = server.dispatch(json.loads(line), transport=transport)
                if response is not None:
                    transport.write(response)
        finally:
            for supervisor in supervisors:
                supervisor.shutdown()
            server._shutdown_sessions()


if __name__ == "__main__":
    _serve_parent()
