"""Scripted OpenAI-compatible loopback endpoint with per-request wire receipts."""

import json
import threading
from contextlib import contextmanager
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from time import monotonic_ns
from typing import Final, Iterator

from host_metrics import (
    MeasurementError,
    Process,
    model_socket_inode,
    registered_host,
    require_socket_owner,
)

MODEL: Final = "turn-overhead-fixture"


@dataclass(slots=True)
class ModelTurn:
    prompt: str
    marker: str
    path: Path
    home: Path
    sid: str
    impl: str
    parent_pid: int
    call_id: str
    sample_cold_pss: bool = False
    requests: list[dict] = field(default_factory=list)

    @property
    def response(self) -> str:
        return f"Fixture complete {self.call_id}"


class Fixture(HTTPServer):
    turn: ModelTurn | None = None
    errors: list[str]


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format: str, *args: str) -> None:
        return

    def do_POST(self) -> None:
        started_ns = monotonic_ns()
        fixture = self.server
        turn = fixture.turn
        if self.path == "/api/show":
            # Hermes probes custom loopback endpoints for Ollama metadata.
            self.send_error(404, "No Ollama metadata")
            return
        try:
            if self.path != "/v1/chat/completions" or turn is None:
                raise MeasurementError(f"unexpected model request to {self.path}")
            self.serve_turn(turn, started_ns)
        except (MeasurementError, OSError, ValueError, KeyError) as exc:
            fixture.errors.append(str(exc))
            self.send_error(422, "Loopback fixture rejected the request")

    def serve_turn(self, turn: ModelTurn, started_ns: int) -> None:
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        messages = body["messages"]
        user_index = max(i for i, message in enumerate(messages) if message["role"] == "user")
        user = messages[user_index]["content"]
        if turn.prompt not in json.dumps(user) or body["model"] != MODEL:
            raise MeasurementError("model request did not carry the current fixture prompt and model")
        following = messages[user_index + 1:]
        definitions = [tool["function"]["name"] for tool in body.get("tools", [])]
        if definitions != ["read_file"]:
            raise MeasurementError(f"expected only read_file in model tool definitions, got {definitions}")
        phase = len(turn.requests) + 1
        call = {"id": turn.call_id, "type": "function", "function": {
            "name": "read_file", "arguments": json.dumps({"path": str(turn.path)}),
        }}
        tool_result = None
        if phase == 1 and not following:
            message = {"role": "assistant", "tool_calls": [call]}
            reason = "tool_calls"
        elif phase == 2 and [m["role"] for m in following] == ["assistant", "tool"]:
            calls = following[0]["tool_calls"]
            tool_result = following[1]
            if (len(calls) != 1 or calls[0]["id"] != turn.call_id
                    or calls[0]["function"]["name"] != "read_file"
                    or tool_result["tool_call_id"] != turn.call_id
                    or json.dumps(tool_result["content"]).count(turn.marker) != 1):
                raise MeasurementError("second model request lacked the single matching read_file marker")
            message = {"role": "assistant", "content": turn.response}
            reason = "stop"
        else:
            raise MeasurementError(f"unexpected model exchange phase {phase}, tail roles "
                                   f"{[m['role'] for m in following]}")

        parent = Process.read(turn.parent_pid)
        host = registered_host(turn.home, turn.sid, turn.impl)
        if turn.impl != "inproc" and (host is None or host.ppid != parent.pid):
            raise MeasurementError(f"{turn.impl} has no registered direct host child for this turn")
        owner = host or parent
        identity = owner.identity()
        if turn.impl == "python" and " -m tui_gateway.compute_host" not in identity["entry"]:
            raise MeasurementError(f"expected Python compute host, observed {identity}")
        if turn.impl == "rust" and Path(identity["exe"]).name != "lina-turn-host":
            raise MeasurementError(f"expected Rust compute host, observed {identity}")
        inode = model_socket_inode(self.client_address[1], self.server.server_port)
        require_socket_owner(owner, inode)
        cold_live_pss = None
        if phase == 1 and turn.sample_cold_pss:
            cold_live_pss = {"parent": parent.pss_kib(), "host": host.pss_kib() if host else None}

        stream = body.get("stream", False)
        if stream:
            delta = dict(message)
            if "tool_calls" in message:
                delta["tool_calls"] = [{"index": 0, **call}]
            frames = [
                self.chunk(turn, delta, None),
                self.chunk(turn, {}, reason),
            ]
            payload = "".join(f"data: {json.dumps(frame)}\n\n" for frame in frames) + "data: [DONE]\n\n"
        else:
            payload = json.dumps({
                "id": turn.call_id, "object": "chat.completion", "created": 1, "model": MODEL,
                "choices": [{"index": 0, "message": message, "finish_reason": reason}],
            })
        encoded = payload.encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream" if stream else "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)
        self.wfile.flush()
        turn.requests.append({
            "phase": phase, "path": self.path, "stream": stream, "tools": definitions,
            "received_ns": started_ns, "sent_ns": monotonic_ns(),
            "socket_inode": inode, "owner": identity, "tool_result": tool_result,
            "cold_live_pss_kib": cold_live_pss,
        })

    @staticmethod
    def chunk(turn: ModelTurn, delta: dict, reason: str | None) -> dict:
        return {
            "id": turn.call_id, "object": "chat.completion.chunk", "created": 1, "model": MODEL,
            "choices": [{"index": 0, "delta": delta, "finish_reason": reason}],
        }


@contextmanager
def loopback_model() -> Iterator[Fixture]:
    with Fixture(("127.0.0.1", 0), Handler) as fixture:
        fixture.errors = []
        worker = threading.Thread(target=fixture.serve_forever, kwargs={"poll_interval": 0.05})
        worker.start()
        try:
            yield fixture
        finally:
            fixture.shutdown()
            worker.join()
