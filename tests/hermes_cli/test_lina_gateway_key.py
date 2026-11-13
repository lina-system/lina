"""Each agent's gateway key (lina#643): creating an agent gives its profile its own ``API_SERVER_KEY`` and the web
server the same key, a migration re-run accepts that copy, ``lina doctor`` names a mismatch without a value, and a
fresh agent is talked to through the app's talk door with no ``.env`` edit.
"""

from __future__ import annotations

import stat

import httpx
import pytest
from fastapi.testclient import TestClient

from agent.secret_scope import load_env_file
from hermes_cli import lina_deps, lina_talk, web_server_cron
from hermes_cli.lina_gateway_key import key_env, provision
from tests.hermes_cli._work_env import WorkEnv
from tests.hermes_cli.test_lina_agent_talk_api import AgentGateway, _frames
from tests.hermes_cli.test_lina_talk_api import fake_model

pytestmark = pytest.mark.platforms("posix")


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_deps, "lina_root", lambda: env.root)
    for agent in ("alpha", "beta"):
        monkeypatch.delenv(key_env(agent), raising=False)
    monkeypatch.setenv("API_SERVER_KEY", "fake-key-shared")
    return env


def _key(env_path) -> str | None:
    return load_env_file(env_path).get("API_SERVER_KEY")


def _create(name: str):
    from hermes_cli.profiles import create_profile
    return create_profile(name, no_alias=True)


def test_creating_an_agent_gives_its_profile_and_the_web_server_one_new_key(work, capsys):
    alpha, beta = _create("alpha"), _create("beta")
    key = _key(alpha / ".env")
    assert key and len(key) >= 32 and _key(beta / ".env") not in (None, key)
    assert stat.S_IMODE((alpha / ".env").stat().st_mode) == 0o600
    web = load_env_file(work.root / ".env")
    assert web[key_env("alpha")] == key and web[key_env("beta")] == _key(beta / ".env")
    printed = capsys.readouterr()
    assert key not in printed.out + printed.err


def test_an_existing_key_on_either_side_is_left_alone(work):
    profile = work.root / "profiles" / "alpha"
    profile.mkdir(parents=True)
    (profile / ".env").write_text("API_SERVER_KEY=fake-key-own-0123456789\n", encoding="utf-8")
    (work.root / ".env").write_text("LINA_GATEWAY_KEY_ALPHA=fake-key-web-0123456789\n", encoding="utf-8")
    provision(profile, "alpha", work.root)
    assert (profile / ".env").read_text(encoding="utf-8") == "API_SERVER_KEY=fake-key-own-0123456789\n"
    assert (work.root / ".env").read_text(encoding="utf-8") == "LINA_GATEWAY_KEY_ALPHA=fake-key-web-0123456789\n"


def test_a_migrated_agent_gets_its_key_and_a_rerun_accepts_that_copy(work, tmp_path):
    from hermes_cli.migrate_memory import _publish_files
    copied = tmp_path / "copied"
    copied.mkdir()
    (copied / ".env").write_text("OPENAI_API_KEY=fake-key-provider\n", encoding="utf-8")
    destination = work.root / "profiles" / "alpha-copy"
    destination.mkdir(parents=True)
    _publish_files(copied, destination)
    provision(destination, "alpha-copy", work.root)  # what _profile does after publishing
    key = _key(destination / ".env")
    assert key and load_env_file(destination / ".env")["OPENAI_API_KEY"] == "fake-key-provider"
    assert load_env_file(work.root / ".env")["LINA_GATEWAY_KEY_ALPHA_COPY"] == key
    assert _publish_files(copied, destination) == [".env"]
    (copied / ".env").write_text("OPENAI_API_KEY=fake-key-changed\n", encoding="utf-8")
    with pytest.raises(ValueError, match="destination file differs: .env"):
        _publish_files(copied, destination)


def test_doctor_names_each_agent_whose_key_the_web_server_does_not_hold(work, monkeypatch, capsys):
    from hermes_cli.doctor_state import _check_agent_gateway_keys
    alpha, beta = _create("alpha"), _create("beta")
    assert _check_agent_gateway_keys(False).manual_issues == []
    secrets = [_key(alpha / ".env"), _key(beta / ".env")]
    (alpha / ".env").write_text("# no key\n", encoding="utf-8")
    monkeypatch.setenv(key_env("beta"), "fake-key-stale")
    found = _check_agent_gateway_keys(False)
    assert sorted(found.manual_issues) == [
        "agent 'alpha': no API_SERVER_KEY in its .env",
        "agent 'beta': its API_SERVER_KEY differs from the web server's LINA_GATEWAY_KEY_BETA"]
    printed = capsys.readouterr().out
    assert all(secret not in printed for secret in secrets) and "fake-key" not in printed


class ProfileDoor(AgentGateway):
    """A named profile's gateway door: only the profile's own ``API_SERVER_KEY``, read from its ``.env`` at each
    request (``APIServerAdapter._expected_api_key``); none set refuses every key."""

    def __init__(self, home):
        super().__init__("")
        self.home = home

    def handle(self, request: httpx.Request) -> httpx.Response:
        self.KEY = _key(self.home / ".env") or ""
        return super().handle(request)


def test_a_fresh_agent_is_talked_to_through_the_talk_door_with_no_env_edit(work, monkeypatch):
    from hermes_cli import web_server
    door = ProfileDoor(_create("alpha"))
    monkeypatch.setattr(web_server_cron, "_gateway_fire_endpoint",
                        lambda agent, home: f"http://127.0.0.1:8642/p/{agent}/api/cron/fire")
    monkeypatch.setattr(lina_talk, "http_client",
                        lambda: httpx.AsyncClient(transport=httpx.MockTransport(door.handle)))
    client = TestClient(web_server.app)
    client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    assert client.get("/api/lina/agents/alpha/talk").status_code == 200
    resp = client.post("/api/lina/agents/alpha/talk", json={"message": "안녕"})
    assert resp.status_code == 200
    frames = _frames(resp.text)
    assert frames[0]["data"]["agent"] == "alpha" and frames[5]["data"]["content"] == fake_model("안녕")
