"""The web app build (lina-app) served by the dashboard under ``/app/`` on the client API's origin (lina#168).

The page gets the dashboard's own bootstrap script, so the credential it reads is the one ``/api/lina/*`` accepts.
"""

from __future__ import annotations

import json
import re

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from hermes_cli import web_server
from hermes_cli.dashboard_auth import clear_providers, register_provider
from hermes_cli.web_server_dashboard import _IMMUTABLE_ASSET_CACHE_CONTROL, mount_spa
from tests.hermes_cli.conftest_dashboard_auth import StubAuthProvider
from tests.hermes_cli._work_env import WorkEnv
from tests.hermes_cli.test_lina_board_api import DEVICES, Fakes

pytestmark = pytest.mark.platforms("posix")

APP_HTML = ('<html><head><script type="module" src="/app/assets/index-abc123.js"></script></head>'
            '<body><div id="root"></div></body></html>')
ASSET = "index-abc123.js"


def _build(root):
    (root / "assets").mkdir(parents=True)
    (root / "index.html").write_text(APP_HTML, encoding="utf-8")
    (root / "assets" / ASSET).write_text("console.log('app')", encoding="utf-8")
    return root


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.config(extra=DEVICES)
    return env


@pytest.fixture
def dashboard(tmp_path, monkeypatch):
    """The real server app with a scratch dashboard build, so ``/`` is answerable."""
    dist = tmp_path / "dashboard_dist"
    dist.mkdir()
    (dist / "index.html").write_text("<html><head></head><body>Dashboard</body></html>", encoding="utf-8")
    monkeypatch.setattr(web_server.app.state, "web_dist", dist, raising=False)
    monkeypatch.delenv("LINA_APP_DIST", raising=False)
    return TestClient(web_server.app)


def test_the_configured_build_is_served_with_the_dashboard_credential(work, tmp_path, monkeypatch, dashboard):
    Fakes(work, monkeypatch)
    build = _build(tmp_path / "lina-app")
    work.config(extra=DEVICES + f"dashboard: {{app_dist: {build}}}\n")

    page = dashboard.get("/app/")
    assert page.status_code == 200
    assert '<div id="root">' in page.text
    assert "no-store" in page.headers["cache-control"]
    assert 'window.__HERMES_AUTH_REQUIRED__=false;' in page.text
    assert 'window.__HERMES_BASE_PATH__="";' in page.text
    assert dashboard.get("/app").text == page.text

    route = dashboard.get("/app/some/route")
    assert route.status_code == 200 and route.text == page.text

    asset = dashboard.get(f"/app/assets/{ASSET}")
    assert asset.status_code == 200 and asset.text == "console.log('app')"
    assert asset.headers["cache-control"] == _IMMUTABLE_ASSET_CACHE_CONTROL
    assert dashboard.get("/app/assets/missing-000.js").status_code == 404
    (tmp_path / "secret.txt").write_text("secret", encoding="utf-8")
    assert "secret" not in dashboard.get("/app/..%2Fsecret.txt").text

    token = re.search(r'window\.__HERMES_SESSION_TOKEN__="([^"]+)"', page.text).group(1)
    assert dashboard.get("/api/lina/board").status_code == 401
    board = dashboard.get("/api/lina/board", headers={"X-Hermes-Session-Token": token})
    assert board.status_code == 200 and board.json()["errors"] == []


def test_the_env_folder_overrides_the_config_and_a_proxy_prefix_reaches_the_page(work, tmp_path, monkeypatch,
                                                                                 dashboard):
    work.config(extra=DEVICES + f"dashboard: {{app_dist: {tmp_path / 'nowhere'}}}\n")
    monkeypatch.setenv("LINA_APP_DIST", str(_build(tmp_path / "lina-app")))

    page = dashboard.get("/app/board", headers={"X-Forwarded-Prefix": "/hermes"})
    assert page.status_code == 200
    assert 'window.__HERMES_BASE_PATH__="/hermes";' in page.text
    assert 'src="/hermes/app/assets/index-abc123.js"' in page.text


@pytest.mark.parametrize("configured", ["unset", "missing"])
def test_no_build_answers_one_404_line_and_the_dashboard_still_works(work, tmp_path, dashboard, configured):
    if configured == "missing":
        work.config(extra=DEVICES + f"dashboard: {{app_dist: {tmp_path / 'not-built'}}}\n")

    for path in ("/app/", "/app/some/route", f"/app/assets/{ASSET}"):
        response = dashboard.get(path, headers={web_server._SESSION_HEADER_NAME: web_server._SESSION_TOKEN})
        assert response.status_code == 404, path
        assert "\n" not in response.text.strip() and "error" in json.loads(response.text), path

    root = dashboard.get("/")
    assert root.status_code == 200 and "Dashboard" in root.text


def test_the_gated_dashboard_signs_in_for_the_app_as_for_its_own_page(work, tmp_path, monkeypatch, dashboard):
    Fakes(work, monkeypatch)
    monkeypatch.setenv("LINA_APP_DIST", str(_build(tmp_path / "lina-app")))
    clear_providers()
    register_provider(StubAuthProvider())
    for key, value in (("bound_host", "fly-app.fly.dev"), ("bound_port", 443), ("auth_required", True)):
        monkeypatch.setattr(web_server.app.state, key, value, raising=False)

    def client():
        return TestClient(web_server.app, base_url="https://fly-app.fly.dev")
    try:
        own, app = (client().get(path, follow_redirects=False) for path in ("/", "/app/"))
        assert own.status_code == app.status_code and 300 <= app.status_code < 400
        assert own.headers["location"].split("?")[0] == app.headers["location"].split("?")[0]

        signed_in = client()
        start = signed_in.get("/auth/login?provider=stub", follow_redirects=False)
        state = start.headers["location"].split("state=")[1]
        assert signed_in.get(f"/auth/callback?code=stub_code&state={state}", follow_redirects=False).status_code == 302
        page = signed_in.get("/app/some/route")
        assert page.status_code == 200 and '<div id="root">' in page.text
        assert "window.__HERMES_AUTH_REQUIRED__=true;" in page.text
        assert web_server._SESSION_TOKEN not in page.text
        assert signed_in.get("/api/lina/board").status_code == 200
    finally:
        clear_providers()


@pytest.mark.parametrize("headless", [False, True], ids=["dashboard", "serve"])
def test_only_the_dashboard_serves_the_app_never_the_headless_backend(tmp_path, monkeypatch, headless):
    monkeypatch.setenv("LINA_APP_DIST", str(_build(tmp_path / "lina-app")))
    if headless:
        monkeypatch.setenv("HERMES_SERVE_HEADLESS", "1")
    else:
        monkeypatch.delenv("HERMES_SERVE_HEADLESS", raising=False)
    app = FastAPI()
    mount_spa(app)

    response = TestClient(app).get("/app/")
    assert (response.status_code, '<div id="root">' in response.text) == ((404, False) if headless else (200, True))
