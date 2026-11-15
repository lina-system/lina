"""A timed-out dialectic query is sent once; reads and writes keep the SDK's retries (#714).

The server does not cancel a non-streaming dialectic when the client gives up, so every SDK retry of
a timed-out chat starts the same 40-60 s dialectic again. Runs the real SDK over a counting fake
transport.
"""

import json

import httpx
import pytest

from plugins.memory.honcho import client as client_mod
from plugins.memory.honcho.client import HonchoClientConfig
from plugins.memory.honcho.oauth import apply_token_to_client
from plugins.memory.honcho.session import HonchoSession, HonchoSessionManager

sdk_http = pytest.importorskip("honcho.http.client", reason="honcho SDK not installed")

CHAT = "/v3/workspaces/w/peers/assistant/chat"
CONCLUSIONS = "/v3/workspaces/w/conclusions"


@pytest.fixture
def sent(monkeypatch):
    """Every request the SDK sends, as (method, path, Authorization); chat and conclusion writes time out."""
    requests: list[tuple[str, str, str]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append((request.method, request.url.path, request.headers.get("Authorization", "")))
        if request.url.path in (CHAT, CONCLUSIONS):
            raise httpx.ReadTimeout("stalled", request=request)
        if request.url.path.endswith("/peers"):
            peer_id = json.loads(request.content)["id"]
            return httpx.Response(200, json={"id": peer_id, "workspace_id": "w", "created_at": "2026-01-01T00:00:00Z"})
        return httpx.Response(200, json={})

    transport = httpx.MockTransport(handler)
    real_client = httpx.Client

    class _FakeTransportClient(real_client):
        def __init__(self, *args, **kwargs):
            super().__init__(*args, transport=transport, **kwargs)

    monkeypatch.setattr(httpx, "Client", _FakeTransportClient)
    monkeypatch.setattr(sdk_http.HonchoHTTPClient, "_get_retry_delay", lambda self, attempt, retry_after=None: 0.0)
    client_mod.reset_honcho_client()
    yield requests
    client_mod.reset_honcho_client()


@pytest.fixture
def manager(sent):
    cfg = HonchoClientConfig(api_key="test-key", base_url="https://honcho.invalid", timeout=30.0, workspace_id="w",
                             write_frequency="turn", enabled=True)
    mgr = HonchoSessionManager(config=cfg)
    mgr._cache["k"] = HonchoSession("k", "user", "assistant", "sid")
    yield mgr
    mgr.close_dialectic()


def _posts(sent, path):
    return [r for r in sent if r[:2] == ("POST", path)]


def test_a_timed_out_chat_is_sent_once_and_a_timed_out_write_keeps_its_retries(manager, sent):
    assert manager.dialectic_query("k", "who is the user?") == ""
    assert len(_posts(sent, CHAT)) == 1

    assert manager.create_conclusion("k", "prefers tea") is False
    assert len(_posts(sent, CONCLUSIONS)) == 3  # the SDK default: one attempt and two retries


def test_the_chat_client_sends_the_shared_clients_current_bearer(manager, sent):
    manager.dialectic_query("k", "who is the user?")
    apply_token_to_client(manager.honcho, "rotated-token")  # what an OAuth refresh does to the shared client
    manager.dialectic_query("k", "who is the user?")

    assert [auth for _, _, auth in _posts(sent, CHAT)] == ["Bearer test-key", "Bearer rotated-token"]
