"""``GET /api/lina/agents/{agent}/avatar`` and the people list's ``avatar`` flag (lina#738) over a scratch LINA root:
each agent's picture is its profile's ``assets/avatar.<ext>``, made here as a tiny image, and the list validates
against ``docs/api/board-client.yaml``."""

from __future__ import annotations

import io
import os

import pytest
from fastapi.testclient import TestClient
from PIL import Image

from hermes_cli import lina_avatar, lina_deps
from tests.hermes_cli._work_env import WorkEnv
from tests.hermes_cli.test_lina_board_items import _valid

pytestmark = pytest.mark.platforms("posix")


def _image(side: int, fmt: str = "PNG") -> bytes:
    out = io.BytesIO()
    Image.new("RGBA" if fmt != "JPEG" else "RGB", (side, side), (120, 60, 200)).save(out, fmt)
    return out.getvalue()


def _noise_png(side: int) -> bytes:
    out = io.BytesIO()
    Image.frombytes("RGBA", (side, side), os.urandom(side * side * 4)).save(out, "PNG")
    return out.getvalue()


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_deps, "lina_root", lambda: env.root)
    return env


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _avatar(work: WorkEnv, agent: str, blob: bytes, ext: str = "png"):
    assets = work.root / "profiles" / agent / "assets"
    assets.mkdir(parents=True, exist_ok=True)
    (assets / f"avatar.{ext}").write_bytes(blob)
    return assets / f"avatar.{ext}"


def _people(client) -> dict:
    response = client.get("/api/lina/work/items")
    assert response.status_code == 200, response.text
    return {p["id"]: p for p in _valid("ItemList", response.json())["agents"]}


def test_a_picture_is_served_downsized_with_private_caching_and_a_matching_etag_answers_304(work, client):
    _avatar(work, "alpha", _image(160))
    response = client.get("/api/lina/agents/alpha/avatar")
    assert response.status_code == 200
    assert response.headers["content-type"] == "image/png"
    assert response.headers["cache-control"] == "private, max-age=3600"
    assert Image.open(io.BytesIO(response.content)).size == (128, 128)
    again = client.get("/api/lina/agents/alpha/avatar", headers={"If-None-Match": response.headers["etag"]})
    assert (again.status_code, again.content, again.headers["etag"]) == (304, b"", response.headers["etag"])


@pytest.mark.parametrize("blob, ext, media, size", [
    (_image(64, "JPEG"), "jpg", "image/jpeg", (64, 64)),
    (_image(300, "JPEG"), "jpg", "image/jpeg", (128, 128)),
    (_image(200, "WEBP"), "webp", "image/png", (128, 128)),
])
def test_a_jpeg_stays_a_jpeg_a_webp_becomes_a_png_and_a_small_one_is_served_as_stored(work, client, blob, ext,
                                                                                        media, size):
    _avatar(work, "beta", blob, ext)
    response = client.get("/api/lina/agents/beta/avatar")
    assert (response.status_code, response.headers["content-type"]) == (200, media)
    assert Image.open(io.BytesIO(response.content)).size == size
    assert (response.content == blob) == (size == (64, 64))


def test_no_picture_and_an_agent_not_in_the_list_answer_404(work, client):
    _avatar(work, "gamma", _image(32))  # a profile with a picture that the record does not list
    for agent in ("beta", "gamma"):
        response = client.get(f"/api/lina/agents/{agent}/avatar")
        assert response.status_code == 404
        assert "detail" in response.json()


@pytest.mark.parametrize("blob", [b"", b"just some text\n", b"\x89PNG\r\n\x1a\n" + b"\0" * 64, _noise_png(600)],
                         ids=["empty", "text", "png-magic-only", "over-1mb"])
def test_a_file_that_is_not_a_picture_is_no_picture(work, client, blob):
    _avatar(work, "alpha", blob)
    assert client.get("/api/lina/agents/alpha/avatar").status_code == 404
    assert "avatar" not in _people(client)["alpha"]


def test_the_people_list_says_avatar_only_for_an_agent_with_a_picture(work, client):
    _avatar(work, "alpha", _image(160))
    people = _people(client)
    assert people["alpha"] == {"id": "alpha", "name": "알파", "avatar": True}
    assert people["beta"] == {"id": "beta", "name": "베타"}


def test_a_path_never_comes_from_the_request(work, client, tmp_path):
    outside = tmp_path / "outside.png"
    outside.write_bytes(_image(32))
    _avatar(work, "alpha", b"").unlink()
    (work.root / "profiles" / "alpha" / "assets" / "avatar.png").symlink_to(outside)
    (work.root / "escape" / "assets").mkdir(parents=True)
    (work.root / "escape" / "assets" / "avatar.png").write_bytes(_image(32))
    config = work.root / "config.yaml"
    config.write_text(config.read_text(encoding="utf-8").replace(
        "agents: {alpha: 알파, beta: 베타}", f'agents: {{alpha: 알파, "../escape": e, "{outside}": o}}'),
        encoding="utf-8")
    assert client.get("/api/lina/agents/alpha/avatar").status_code == 404
    assert "avatar" not in _people(client)["alpha"]
    for agent in ("../escape", str(outside)):
        assert lina_avatar.source(agent) is None
    for path in ("/api/lina/agents/..%2Fescape/avatar", "/api/lina/agents/%2Ftmp%2Foutside.png/avatar"):
        response = client.get(path)
        assert response.status_code == 404 and not response.headers["content-type"].startswith("image/")


def test_the_route_is_behind_the_dashboard_auth_gate(work):
    from hermes_cli import web_server
    _avatar(work, "alpha", _image(32))
    assert TestClient(web_server.app).get("/api/lina/agents/alpha/avatar").status_code == 401
