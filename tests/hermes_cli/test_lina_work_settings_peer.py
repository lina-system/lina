"""Who the settings write gate sees behind the hub's own ``tailscale serve`` (lina#283): a loopback peer is judged
by the last ``X-Forwarded-For`` entry, any other peer by itself, and the per-device rate limit counts the forwarded
device. ``tailscale whois`` is the fake of the API tests; addresses and logins are synthetic."""

from __future__ import annotations

import pytest
from fastapi.testclient import TestClient
from starlette.requests import Request

from hermes_cli.web_routers.lina_work_settings import _peer
from tests.hermes_cli.test_lina_work_settings_api import BASE, OWNER, Env

pytestmark = pytest.mark.platforms("posix")
DEVICE = "100.64.0.9"


def _request(host: str, forwarded: str | None = None) -> Request:
    headers = [(b"x-forwarded-for", forwarded.encode())] if forwarded is not None else []
    return Request({"type": "http", "method": "GET", "path": "/", "headers": headers, "client": (host, 4242)})


def test_the_peer_is_the_forwarded_device_only_behind_a_loopback_peer():
    cases = [("127.0.0.1", DEVICE), ("::1", f"203.0.113.5, {DEVICE} "), ("100.64.0.20", DEVICE),
             ("127.0.0.1", None), ("127.0.0.1", "not-an-address")]

    seen = [_peer(_request(host, forwarded)) for host, forwarded in cases]

    assert seen == [DEVICE, DEVICE, "100.64.0.20", "127.0.0.1", "127.0.0.1"]


@pytest.fixture
def loopback(tmp_path, monkeypatch):
    from hermes_cli import web_server
    env = Env(tmp_path, monkeypatch)
    env.whois({DEVICE: (OWNER, "phone-a")})
    client = TestClient(web_server.app, client=("127.0.0.1", 50000))
    client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    client.headers["X-Forwarded-For"] = DEVICE
    return client


def test_behind_tailscale_serve_the_owner_device_writes_and_its_31st_write_in_a_minute_is_429(loopback):
    view = loopback.get(BASE)
    assert view.json()["writable"] is True
    etag = view.headers["ETag"]
    for n in range(30):
        reply = loopback.patch(BASE, json={"devices": {"workers": {"build-a": {"cap": n + 3}}}},
                               headers={"If-Match": etag})
        assert reply.status_code == 200, (n, reply.text)
        etag = reply.headers["ETag"]
    over = loopback.patch(BASE, json={"devices": {"workers": {"build-a": {"cap": 99}}}}, headers={"If-Match": etag})
    assert over.status_code == 429
