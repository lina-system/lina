"""The owner's agent nodes (lina#574): a device in ``worker.settings.owner.agents`` writes the work settings through
the page path whatever its whois login, behind the hub's own ``tailscale serve`` (a loopback peer plus
``X-Forwarded-For``, lina#283). The fake ``tailscale whois`` is the API tests'; names and addresses are synthetic."""

from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from tests.hermes_cli.test_lina_work_settings_api import BASE, OWNER, Env, _config
from tests.hermes_cli.test_lina_work_settings_cli import _lina

pytestmark = pytest.mark.platforms("posix")

TAGGED = "tagged-devices"  # whois gives a tagged node this login, never the owner's
AGENT, PHONE, STRANGER, MASKED, UNKNOWN = "100.64.0.10", "100.64.0.11", "100.64.0.12", "100.64.0.13", "100.64.0.14"
WHOIS = {AGENT: (TAGGED, "agent-node"), PHONE: (OWNER, "phone-a"), STRANGER: (TAGGED, "other-node"),
         MASKED: ("someone@example.test", "PHONE-A")}
LISTED = f"{{login: {OWNER}, devices: [phone-a], agents: [Agent-Node]}}"


def _gate(env: Env, owner: str) -> None:
    from hermes_cli import lina_work_settings_gate
    (env.root / "config.yaml").write_text(
        _config(False, env.tool) + f"  settings:\n    owner: {owner}\n    tailscale: {env.tool}\n", encoding="utf-8")
    lina_work_settings_gate.GATE.reset()


@pytest.fixture
def hub(tmp_path, monkeypatch):
    from hermes_cli import web_server
    env = Env(tmp_path, monkeypatch)
    env.whois(WHOIS)
    _gate(env, LISTED)
    client = TestClient(web_server.app, client=("127.0.0.1", 50000))
    client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return env, client


def _write(client, peer: str, cap: int) -> tuple[bool, int]:
    view = client.get(BASE, headers={"X-Forwarded-For": peer})
    reply = client.patch(BASE, json={"devices": {"workers": {"build-a": {"cap": cap}}}},
                         headers={"X-Forwarded-For": peer, "If-Match": view.headers["ETag"]})
    return view.json()["writable"], reply.status_code


def test_an_agent_node_writes_whatever_its_login_and_its_31st_write_in_a_minute_is_429(hub):
    env, client = hub
    view = client.get(BASE, headers={"X-Forwarded-For": AGENT})
    assert view.json()["writable"] is True
    etag = view.headers["ETag"]
    for n in range(30):
        reply = client.patch(BASE, json={"devices": {"workers": {"build-a": {"cap": n + 3}}}},
                             headers={"X-Forwarded-For": AGENT, "If-Match": etag})
        assert reply.status_code == 200, (n, reply.text)
        etag = reply.headers["ETag"]
    over = client.patch(BASE, json={"devices": {"workers": {"build-a": {"cap": 99}}}},
                        headers={"X-Forwarded-For": AGENT, "If-Match": etag})
    assert over.status_code == 429
    assert env.config()["devices"]["workers"]["build-a"]["cap"] == 32
    by = client.get(f"{BASE}/history?limit=1").json()["revisions"][0]["by"]
    assert by == {"kind": "app", "principal": TAGGED, "device": "agent-node"}
    assert _write(client, PHONE, 40) == (True, 200)  # the limit is per device: the owner's phone still writes


def test_an_agent_listed_name_with_the_owner_login_writes_and_agents_need_no_owner_login(hub):
    env, client = hub
    _gate(env, f"{{login: {OWNER}, devices: [phone-z], agents: [phone-a]}}")
    assert _write(client, PHONE, 5) == (True, 200)
    _gate(env, "{agents: [agent-node]}")
    assert _write(client, AGENT, 6) == (True, 200)
    assert _write(client, PHONE, 7) == (False, 403)
    assert env.config()["devices"]["workers"]["build-a"]["cap"] == 6


@pytest.mark.parametrize("peer", [STRANGER, MASKED, UNKNOWN], ids=["unlisted-tagged-node", "other-login-on-owner-device",
                                                                   "whois-fails"])
def test_a_peer_in_neither_list_or_unknown_to_whois_is_refused_while_the_agent_node_writes(hub, peer):
    env, client = hub
    assert _write(client, AGENT, 5) == (True, 200)
    assert _write(client, peer, 9) == (False, 403)
    assert env.config()["devices"]["workers"]["build-a"]["cap"] == 5


def test_with_agents_empty_the_gate_is_today_s(hub):
    env, client = hub
    assert _write(client, AGENT, 5) == (True, 200)
    _gate(env, f"{{login: {OWNER}, devices: [phone-a], agents: []}}")
    assert _write(client, AGENT, 6) == (False, 403)
    assert _write(client, PHONE, 7) == (True, 200)
    _gate(env, "{agents: []}")
    assert _write(client, PHONE, 8) == (False, 403)


def test_neither_the_api_nor_the_cli_changes_the_agents_list(hub, capsys):
    env, client = hub
    etag = client.get(BASE, headers={"X-Forwarded-For": AGENT}).headers["ETag"]
    widen = {"worker": {"settings": {"owner": {"agents": ["agent-node", "other-node"]}}}}
    reply = client.patch(BASE, json=widen, headers={"X-Forwarded-For": AGENT, "If-Match": etag})
    assert reply.status_code == 422
    assert [e["path"] for e in reply.json()["errors"]] == ["worker.settings"]
    capsys.readouterr()
    assert _lina("settings", "set", "worker.settings.owner.agents", "[agent-node, other-node]") == 2
    assert "worker.settings" in capsys.readouterr().err
    assert env.config()["worker"]["settings"]["owner"]["agents"] == ["Agent-Node"]
    assert _write(client, STRANGER, 9) == (False, 403)
