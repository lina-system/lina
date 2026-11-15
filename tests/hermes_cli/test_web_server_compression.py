"""Compressed responses and cache lifetimes of the web server (lina#729).

JSON and static responses go out gzip-encoded when the client accepts it and the body is worth it; streams (SSE,
WebSocket) and bodies already encoded pass untouched. Hashed bundle assets are cached for a year, files without a
content hash are revalidated.
"""

from __future__ import annotations

import gzip
import json

import httpx
import pytest
from fastapi import FastAPI, WebSocket
from fastapi.responses import JSONResponse, Response, StreamingResponse
from fastapi.testclient import TestClient

from hermes_cli import web_server
from hermes_cli.web_server_compression import MINIMUM_SIZE, add_compression
from hermes_cli.web_server_dashboard import _IMMUTABLE_ASSET_CACHE_CONTROL

pytestmark = pytest.mark.platforms("posix")

BIG = {"rows": [{"id": n, "title": f"synthetic row {n}"} for n in range(200)]}
SCRIPT = "console.log('app');\n" * 200


def _raw(client: TestClient, path: str, encoding: str = "gzip", **headers) -> httpx.Response:
    """The response as it went over the wire: httpx decodes gzip by itself, so read the undecoded stream."""
    with client.stream("GET", path, headers={"Accept-Encoding": encoding, **headers}) as r:
        r.raw_body = b"".join(r.iter_raw())
    return r


@pytest.fixture
def small_app():
    app = FastAPI()
    add_compression(app)

    @app.get("/big")
    def big():
        return BIG

    @app.get("/small")
    def small():
        return {"ok": True}

    @app.get("/encoded")
    def encoded():
        return Response(gzip.compress(json.dumps(BIG).encode()), media_type="application/json",
                        headers={"Content-Encoding": "gzip"})

    @app.get("/events")
    def events():
        return StreamingResponse(iter([f"data: {'x' * 2000}\n\n"]), media_type="text/event-stream")

    @app.websocket("/ws")
    async def ws(socket: WebSocket):
        await socket.accept()
        await socket.send_text(json.dumps(BIG))
        await socket.close()

    return TestClient(app)


def test_a_json_body_is_gzipped_only_when_the_client_accepts_it_and_it_is_worth_it(small_app):
    raw = json.dumps(BIG).encode()
    assert len(raw) >= MINIMUM_SIZE
    zipped = _raw(small_app, "/big")
    assert zipped.headers["content-encoding"] == "gzip" and "accept-encoding" in zipped.headers["vary"].lower()
    assert json.loads(gzip.decompress(zipped.raw_body)) == BIG and len(zipped.raw_body) < len(raw) / 3
    assert int(zipped.headers["content-length"]) == len(zipped.raw_body)

    plain = _raw(small_app, "/big", encoding="identity")
    assert "content-encoding" not in plain.headers and json.loads(plain.raw_body) == BIG
    tiny = _raw(small_app, "/small")
    assert "content-encoding" not in tiny.headers and json.loads(tiny.raw_body) == {"ok": True}


def test_streams_and_encoded_bodies_pass_untouched(small_app):
    encoded = _raw(small_app, "/encoded")
    assert encoded.headers["content-encoding"] == "gzip" and json.loads(gzip.decompress(encoded.raw_body)) == BIG
    events = _raw(small_app, "/events")
    assert "content-encoding" not in events.headers and events.raw_body.startswith(b"data: xxx")
    with small_app.websocket_connect("/ws") as socket:
        assert json.loads(socket.receive_text()) == BIG


@pytest.fixture
def dashboard(tmp_path, monkeypatch):
    """The real server app over a scratch dashboard build and a scratch app build."""
    dist = tmp_path / "dashboard_dist"
    (dist / "assets").mkdir(parents=True)
    (dist / "index.html").write_text("<html><head></head><body>" + "Dashboard " * 200 + "</body></html>",
                                     encoding="utf-8")
    (dist / "assets" / "index-abc123.js").write_text(SCRIPT, encoding="utf-8")
    (dist / "site.webmanifest").write_text(json.dumps({"name": "x" * 2000}), encoding="utf-8")
    app_dist = tmp_path / "lina-app"
    (app_dist / "assets").mkdir(parents=True)
    (app_dist / "index.html").write_text("<html><head></head><body>" + "App " * 400 + "</body></html>",
                                         encoding="utf-8")
    (app_dist / "assets" / "index-def456.js").write_text(SCRIPT, encoding="utf-8")
    (app_dist / "manifest.webmanifest").write_text(json.dumps({"name": "y" * 2000}), encoding="utf-8")
    monkeypatch.setattr(web_server.app.state, "web_dist", dist, raising=False)
    monkeypatch.setenv("LINA_APP_DIST", str(app_dist))
    return TestClient(web_server.app)


def test_the_server_gzips_its_json_pages_and_assets(dashboard):
    schema = _raw(dashboard, "/api/config/schema", **{web_server._SESSION_HEADER_NAME: web_server._SESSION_TOKEN})
    assert schema.status_code == 200 and schema.headers["content-encoding"] == "gzip"
    assert json.loads(gzip.decompress(schema.raw_body))
    for path in ("/", "/app/", "/assets/index-abc123.js", "/app/assets/index-def456.js"):
        r = _raw(dashboard, path)
        assert r.status_code == 200 and r.headers["content-encoding"] == "gzip", path
        assert len(r.raw_body) < len(gzip.decompress(r.raw_body)), path


def test_the_server_sends_a_small_reply_as_it_is(dashboard):
    small = _raw(dashboard, "/api/no-such-route", **{web_server._SESSION_HEADER_NAME: web_server._SESSION_TOKEN})
    assert small.status_code == 404 and len(small.raw_body) < MINIMUM_SIZE
    assert "content-encoding" not in small.headers and int(small.headers["content-length"]) == len(small.raw_body)


def test_hashed_assets_live_a_year_and_files_without_a_hash_are_revalidated(dashboard):
    for path in ("/assets/index-abc123.js", "/app/assets/index-def456.js"):
        assert dashboard.get(path).headers["cache-control"] == _IMMUTABLE_ASSET_CACHE_CONTROL, path
    for path in ("/", "/app/"):
        assert "no-store" in dashboard.get(path).headers["cache-control"], path
    for path in ("/site.webmanifest", "/app/manifest.webmanifest"):
        r = dashboard.get(path)
        assert r.status_code == 200 and r.headers["cache-control"] == "no-cache", path
