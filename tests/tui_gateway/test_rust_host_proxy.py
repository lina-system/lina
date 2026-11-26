"""Observe the Rust model connection at both ends of a loopback proxy."""

from __future__ import annotations

import base64
import json
import socket
import struct
from collections.abc import Iterator
from contextlib import ExitStack, closing, contextmanager
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from queue import Queue
from threading import Thread
from uuid import uuid4

import pytest

from tests.tui_gateway._turn_host_parity_support import ModelServer, Reply, loopback_model
from tests.tui_gateway.test_rust_host_rollback import rust_binary, rust_binary_on_path
from tests.tui_gateway.test_rust_turn_plan import turn
from tui_gateway.rust_host_supervisor import RustHostSupervisor, select_host

__all__ = ["rust_binary", "rust_binary_on_path", "turn"]
pytestmark = [pytest.mark.platforms("linux"), pytest.mark.usefixtures("real_build_revision", "rust_binary_on_path")]


def run_turn(turn, url, prompt, resources):
    session, template, _ = turn
    agent = session["agent"]
    agent.base_url = agent._client_kwargs["base_url"] = url
    frame = dict(template, request_id=prompt, turn_id=prompt, text=prompt,
                 history=session["history"], history_version=session["history_version"])
    cfg = {"compute_host_impl": "rust", "compute_host_heartbeat_secs": 15, "compute_host_respawn_max": 0}
    with ExitStack() as lease:
        host = select_host(frame["sid"], session, cfg, frame, lease)
        assert isinstance(host, RustHostSupervisor)
        resources.callback(host.shutdown)
        session.update(_compute_host_turn_host=host, _compute_host_turn_id=prompt)
        terminal = Queue()
        host.submit_turn(frame, on_complete=terminal.put)
        details = host.terminal_details(terminal.get(timeout=30))
        host.finish_events(persisted=False)
        return details


@contextmanager
def proxy_for(model: ModelServer, scheme: str) -> Iterator[tuple[str, list[tuple[str, int]]]]:
    connections = []

    class Proxy(BaseHTTPRequestHandler):
        def handle(self):
            if scheme == "socks":
                # Negotiate a single IPv4 CONNECT, then relay the HTTP request in the tunnel.
                self.connection.settimeout(5)
                if self.rfile.read(2) != b"\x05\x01" or self.rfile.read(1) != b"\x00":
                    return
                self.wfile.write(b"\x05\x00")
                target = b"\x05\x01\x00\x01" + socket.inet_aton("127.0.0.1") + struct.pack("!H", model.server_port)
                if self.rfile.read(10) != target:
                    return
                self.wfile.write(b"\x05\x00\x00\x01" + bytes(6))
            super().handle()

        def do_POST(self):
            path = "/v1/chat/completions"
            expected = path if scheme == "socks" else model.url + "/chat/completions"
            if self.path != expected:
                self.send_error(400)
                return
            with closing(HTTPConnection("127.0.0.1", model.server_port, timeout=5)) as upstream:
                upstream.connect()
                connections.append(upstream.sock.getsockname())
                upstream.request("POST", path, self.rfile.read(int(self.headers["Content-Length"])))
                response = upstream.getresponse()
                data = response.read()
                self.send_response(response.status)
                self.send_header("Content-Type", response.getheader("Content-Type"))
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        def log_message(self, *args):
            pass

    with ThreadingHTTPServer(("127.0.0.1", 0), Proxy) as proxy:
        thread = Thread(target=proxy.serve_forever)
        thread.start()
        try:
            yield f"{scheme}://127.0.0.1:{proxy.server_port}", connections
        finally:
            proxy.shutdown()
            thread.join()


@pytest.mark.parametrize("scheme", ["http", "socks"], ids=["http-proxy", "socks5-proxy"])
def test_model_route_respects_no_proxy(turn, monkeypatch, scheme):
    peers = []
    accept = ModelServer.get_request

    def record_accept(server):
        connection, peer = accept(server)
        peers.append(peer)
        return connection, peer

    monkeypatch.setattr(ModelServer, "get_request", record_accept)
    with (
        ExitStack() as resources,
        loopback_model((Reply(text="proxied"), Reply(text="direct"))) as model,
        proxy_for(model, scheme) as (proxy_url, connections),
    ):
        monkeypatch.setenv("HTTPS_PROXY", proxy_url)
        monkeypatch.delenv("no_proxy", raising=False)
        monkeypatch.setenv("NO_PROXY", "")
        result = run_turn(turn, model.url, "first", resources)
        assert result["status"] == "complete" and result["text"] == "proxied"
        assert len(peers) == 1
        assert connections == peers
        monkeypatch.setenv("NO_PROXY", "127.0.0.0/8")
        result = run_turn(turn, model.url, "second", resources)
        assert result["status"] == "complete" and result["text"] == "direct"
        assert len(peers) == len(model.requests) == 2
        assert connections == peers[:1]
        assert not model.errors


@pytest.mark.parametrize("status", [407, 400])
def test_proxy_auth_failure_keeps_credentials_off_the_wire(turn, monkeypatch, caplog, capsys, status):
    password = uuid4().hex
    userinfo = f"fixture:{password}"
    basic = base64.b64encode(userinfo.encode()).decode()
    authenticated = []
    frames = []
    for method in ("_send_frame", "_handle_host_frame"):
        original = getattr(RustHostSupervisor, method)

        def observe(self, message, dispatch=original):
            frames.append(json.dumps(message))
            return dispatch(self, message)

        monkeypatch.setattr(RustHostSupervisor, method, observe)

    class Challenge(BaseHTTPRequestHandler):
        def do_POST(self):
            authenticated.append(self.headers.get("Proxy-Authorization") == f"Basic {basic}")
            data = json.dumps({"error": {"message": f"proxy rejected {proxy_url}"}}).encode()
            self.send_response(status)
            self.send_header("X-RateLimit-Reset", basic)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, *args):
            pass

    with loopback_model((Reply(text="direct"),)) as model, ThreadingHTTPServer(("127.0.0.1", 0), Challenge) as proxy:
        proxy_url = f"http://{userinfo}@127.0.0.1:{proxy.server_port}"
        monkeypatch.setenv("HTTPS_PROXY", proxy_url)
        for name in ("NO_PROXY", "no_proxy"):
            monkeypatch.delenv(name, raising=False)
        with ExitStack() as stack:
            thread = Thread(target=proxy.serve_forever)
            thread.start()
            stack.callback(thread.join)
            stack.callback(proxy.shutdown)
            details = run_turn(turn, model.url, "auth", stack)
        assert authenticated == [True] and not model.requests
        assert details["status"] == "error"
        output = capsys.readouterr()
        surfaces = "\n".join([*frames, json.dumps(details), caplog.text, output.out, output.err])
        if any(private in surfaces for private in (proxy_url, password, basic)):
            pytest.fail("Proxy credentials escaped into a host frame, diagnostic, or error")
