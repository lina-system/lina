"""Shutdown aborts a dialectic query in flight instead of waiting on it (#712).

A server-side dialectic runs 40-60 s, past the 30 s shutdown join, so shutdown has to end the request:
the dialectic thread is gone right after it, and nothing is left running against the server.
"""

import socket
import sys
import threading
from types import SimpleNamespace

import httpx
import pytest

from plugins.memory.honcho import HonchoMemoryProvider
from plugins.memory.honcho import session as session_module
from plugins.memory.honcho.client import HonchoClientConfig
from plugins.memory.honcho.session import HonchoSession, HonchoSessionManager


class _StalledServer:
    """Loopback server that reads one request and never answers, like a dialectic still running."""

    def __init__(self):
        self._listener = socket.create_server(("127.0.0.1", 0))
        self.url = f"http://127.0.0.1:{self._listener.getsockname()[1]}"
        self.request_seen = threading.Event()
        self._conns: list[socket.socket] = []
        threading.Thread(target=self._serve, daemon=True).start()

    def _serve(self):
        try:
            conn, _ = self._listener.accept()
        except OSError:
            return
        self._conns.append(conn)
        conn.recv(65536)
        self.request_seen.set()

    def close(self):
        # shutdown() wakes an accept() still waiting; close() alone does not.
        for sock in (self._listener, *self._conns):
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            sock.close()


class _FakeSDK:
    """Stands in for ``honcho.Honcho``: a client sends through the httpx client it is given (its own when
    none is) and keeps its bearer on ``_http.api_key``."""

    def __init__(self, *, base_url, api_key, timeout, http_client=None, **_kwargs):
        self._http = SimpleNamespace(api_key=api_key)
        self._base_url, self._timeout = base_url, timeout
        self._client = http_client or httpx.Client()

    def peer(self, peer_id, **_kwargs):
        def chat(query, **_kwargs):
            url = f"{self._base_url}/v3/workspaces/w/peers/{peer_id}/chat"
            return self._client.post(url, json={"query": query}, timeout=self._timeout).text

        return SimpleNamespace(id=peer_id, chat=chat)


@pytest.fixture
def server():
    srv = _StalledServer()
    yield srv
    srv.close()


@pytest.fixture
def provider(server, monkeypatch):
    monkeypatch.setitem(sys.modules, "honcho", SimpleNamespace(Honcho=_FakeSDK))
    cfg = HonchoClientConfig(api_key="test-key", base_url=server.url, timeout=30.0, workspace_id="w",
                             write_frequency="turn", enabled=True)
    shared = _FakeSDK(base_url=server.url, api_key="test-key", timeout=30.0)
    monkeypatch.setattr(session_module, "get_honcho_client", lambda *a, **k: shared)
    manager = HonchoSessionManager(config=cfg)
    manager._cache["test-session"] = HonchoSession("test-session", "user", "assistant", "sid")
    prov = HonchoMemoryProvider()
    prov._manager, prov._config = manager, cfg
    prov._session_key, prov._session_initialized = "test-session", True
    return prov


@pytest.mark.allow_network  # loopback only: the stalled server above
def test_shutdown_ends_an_in_flight_dialectic_request(provider, server):
    thread = provider._spawn_dialectic("who is the user?", thread_name="honcho-prewarm-dialectic", fired_at=0,
                                       log_label="dialectic prewarm", use_query_rewrite=False)
    assert server.request_seen.wait(timeout=5.0)

    done = threading.Event()
    threading.Thread(target=lambda: (provider.shutdown(), done.set()), daemon=True).start()
    try:
        assert done.wait(timeout=5.0), "shutdown waited on the dialectic request"
        thread.join(timeout=5.0)
        assert not thread.is_alive()
    finally:
        server.close()
        thread.join(timeout=5.0)
