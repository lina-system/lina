"""The owner's app chat with each agent (lina#482): ``POST`` and ``GET /api/lina/agents/{agent}/talk`` over a scratch
LINA root and a fake multiplexed gateway (one listener, each agent behind ``/p/<agent>/`` with its own key), and the
per-agent gateway key both talk routes send. Streams and bodies are validated against ``docs/api/talk-client.yaml``.
"""

from __future__ import annotations

import json
import logging

import httpx
import pytest
from fastapi.testclient import TestClient

from hermes_cli import lina_ocx, lina_talk, web_server_cron
from tests.hermes_cli._work_env import WorkEnv
from tests.hermes_cli.test_lina_talk_api import FakeGateway, _valid, fake_model

pytestmark = pytest.mark.platforms("posix")

KEYS = {"alpha": "fake-key-alpha", "beta": "fake-key-beta"}
SHARED = "fake-key-shared"


class AgentGateway(FakeGateway):
    """One agent's api_server behind the multiplexed listener, with its own key and the session messages route in
    the shape of ``_handle_session_messages``: at most 500 rows a page from ``offset``, tool rows counted, and the
    turns before ``compacted`` replaced by a hidden summary row unless ``include_compacted`` is true."""

    PAGE = 500

    def __init__(self, key: str):
        super().__init__()
        self.KEY = key
        self.compacted = 0

    def _rows(self, sid: str, include_compacted: bool) -> list[dict]:
        turns = [t for t in self.turns if t["session_id"] == sid]
        rows = [] if include_compacted or not self.compacted else [
            {"role": "user", "content": "", "display_kind": "hidden"}]
        for n, turn in enumerate(turns):
            if n < self.compacted and not include_compacted:
                continue
            rows += [{"role": "user", "content": turn["message"]}, {"role": "tool", "content": "{}"},
                     {"role": "assistant", "content": fake_model(turn["message"])}]
        return [{"id": i + 1, "session_id": sid, "timestamp": 1800000000.0 + i, **row} for i, row in enumerate(rows)]

    def handle(self, request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if request.method == "GET" and path.endswith("/messages"):
            self.calls.append(f"GET {path}")
            if request.headers.get("authorization") != f"Bearer {self.KEY}":
                return httpx.Response(401, json={"error": {"message": "Invalid API key"}})
            sid = path.split("/")[-2]
            params = request.url.params
            rows = self._rows(sid, params.get("include_compacted", "").lower() in ("1", "true"))
            limit = min(int(params.get("limit", self.PAGE)), self.PAGE)
            offset = int(params.get("offset", "0"))
            if params.get("order") != "oldest":
                rows = rows[::-1]
            page = rows[offset:offset + limit]
            return httpx.Response(200, json={"object": "list", "session_id": sid, "data": page,
                                             "pagination": {"limit": limit, "offset": offset,
                                                            "order": params.get("order"), "returned": len(page)}})
        return super().handle(request)


class Multiplexed:
    """The default listener: ``/p/<agent>/…`` goes to that agent's api_server, nothing else answers."""

    def __init__(self, homes: dict):
        self.agents = {name: AgentGateway(key) for name, key in KEYS.items()}
        self.homes = homes

    def handle(self, request: httpx.Request) -> httpx.Response:
        parts = request.url.path.split("/")
        if len(parts) > 2 and parts[1] == "p" and parts[2] in self.agents:
            return self.agents[parts[2]].handle(request)
        return httpx.Response(404, json={"error": {"message": "not found"}})


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: env.root)
    env.goal_and_todos()
    return env


@pytest.fixture
def mux(work, monkeypatch):
    """The real key lookup (``lina_talk.gateway``) against the multiplexed listener the cron forwarder resolves."""
    homes = {name: work.root / "profiles" / name for name in ("alpha", "beta", "gamma")}
    for home in homes.values():
        home.mkdir(parents=True)
    listener = Multiplexed(homes)
    monkeypatch.setattr(lina_talk, "profile_home", lambda agent: homes[agent])
    monkeypatch.setattr(web_server_cron, "_gateway_fire_endpoint",
                        lambda agent, home: f"http://127.0.0.1:8642/p/{agent}/api/cron/fire")
    monkeypatch.setattr(lina_talk, "http_client",
                        lambda: httpx.AsyncClient(transport=httpx.MockTransport(listener.handle)))
    for name, key in KEYS.items():
        monkeypatch.setenv(f"LINA_GATEWAY_KEY_{name.upper()}", key)
    monkeypatch.setenv("API_SERVER_KEY", SHARED)
    return listener


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _frames(text: str) -> list[dict]:
    frames = []
    for block in text.strip().split("\n\n"):
        lines = [line for line in block.splitlines() if not line.startswith(":")]
        if not lines:
            continue
        event = next(line[6:].strip() for line in lines if line.startswith("event:"))
        data = json.loads("\n".join(line[5:].lstrip() for line in lines if line.startswith("data:")))
        frames.append(_valid("AgentTalkEvent", {"event": event, "data": data}))
    return frames


def _talk(client, agent: str, message):
    return client.post(f"/api/lina/agents/{agent}/talk", json={"message": message})


def test_each_agent_sends_its_own_key_and_one_without_sends_the_shared_key(mux, tmp_path):
    (mux.homes["gamma"] / ".env").write_text("API_SERVER_KEY=fake-key-in-profile-env\n", encoding="utf-8")
    assert lina_talk.gateway("alpha") == ("http://127.0.0.1:8642/p/alpha", KEYS["alpha"])
    assert lina_talk.gateway("beta") == ("http://127.0.0.1:8642/p/beta", KEYS["beta"])
    assert lina_talk.gateway("gamma") == ("http://127.0.0.1:8642/p/gamma", SHARED)


def test_each_agents_turn_reaches_that_agent_and_no_other(work, mux, client):
    said = {"alpha": "알파에게 한 말", "beta": "베타에게 한 말"}
    for agent, message in said.items():
        resp = _talk(client, agent, f"  {message}  ")
        assert resp.status_code == 200 and resp.headers["content-type"].startswith("text/event-stream")
        frames = _frames(resp.text)
        assert [f["event"] for f in frames] == ["talk.started", "run.started", "message.started", "assistant.delta",
                                                "assistant.delta", "assistant.completed", "run.completed", "done"]
        assert frames[0]["data"]["agent"] == agent and "item" not in frames[0]["data"]
        assert frames[5]["data"]["content"] == fake_model(message)
    for agent, gw in mux.agents.items():
        (turn,) = gw.turns
        assert turn["message"] == f"[LINA app]\n\n{said[agent]}"
        assert turn["author"] == {"id": "owner", "name": "주인", "is_bot": False}
        assert gw.sessions[turn["session_id"]]["title"] == f"LINA app: {agent}"
        assert all(call.split()[1].startswith(f"/p/{agent}/") for call in gw.calls)
    assert not (mux.homes["alpha"] / "app_talk").exists()  # its own window, not carried into the messenger chat


def test_the_history_route_returns_the_earlier_turns_of_that_app_chat(work, mux, client):
    empty = client.get("/api/lina/agents/alpha/talk")
    assert empty.status_code == 200
    assert _valid("AgentTalkHistory", empty.json()) == {"agent": "alpha", "session_id": None, "messages": []}
    assert mux.agents["alpha"].sessions == {}  # reading creates nothing
    _talk(client, "alpha", "하나")
    _talk(client, "alpha", "둘")
    _talk(client, "beta", "베타 쪽")
    body = _valid("AgentTalkHistory", client.get("/api/lina/agents/alpha/talk").json())
    assert body["agent"] == "alpha" and body["session_id"] in mux.agents["alpha"].sessions
    assert [(m["role"], m["content"]) for m in body["messages"]] == [
        ("user", "[LINA app]\n\n하나"), ("assistant", fake_model("하나")),
        ("user", "[LINA app]\n\n둘"), ("assistant", fake_model("둘"))]


def _history_after(client, gw: AgentGateway, turns: int) -> list[tuple[str, str]]:
    """The window's history once the alpha app conversation holds *turns* turns (the first one run through the
    route, the rest stored as the gateway would)."""
    _talk(client, "alpha", "차례 1")
    (first,) = gw.turns
    gw.turns += [{**first, "message": f"[LINA app]\n\n차례 {n}"} for n in range(2, turns + 1)]
    body = _valid("AgentTalkHistory", client.get("/api/lina/agents/alpha/talk").json())
    return [(m["role"], m["content"]) for m in body["messages"]]


def test_the_history_reaches_the_latest_turn_past_one_page_of_the_gateway(work, mux, client):
    gw = mux.agents["alpha"]
    seen = _history_after(client, gw, 200)  # 600 stored rows with the tool rows: more than one 500-row page
    assert len(seen) == 400
    assert seen[:2] == [("user", "[LINA app]\n\n차례 1"), ("assistant", fake_model("차례 1"))]
    assert seen[-2:] == [("user", "[LINA app]\n\n차례 200"), ("assistant", fake_model("차례 200"))]


def test_the_history_keeps_the_turns_before_a_compaction(work, mux, client):
    gw = mux.agents["alpha"]
    before = _history_after(client, gw, 3)
    gw.compacted = 2
    after = _valid("AgentTalkHistory", client.get("/api/lina/agents/alpha/talk").json())
    assert [(m["role"], m["content"]) for m in after["messages"]] == before
    assert [c for _r, c in before][::2] == [f"[LINA app]\n\n차례 {n}" for n in (1, 2, 3)]


def test_a_refused_key_answers_without_the_key(work, mux, client, monkeypatch, caplog):
    monkeypatch.setenv("LINA_GATEWAY_KEY_ALPHA", "fake-key-stale")
    caplog.set_level(logging.DEBUG)
    answers = [_talk(client, "alpha", "안녕"), client.get("/api/lina/agents/alpha/talk"),
               client.post("/api/lina/work/items/T1/talk", json={"message": "안녕"})]
    for resp in answers:
        assert resp.status_code == 502
        assert _valid("Error", resp.json())["error"] == {
            "code": "gateway_refused_key", "message": "the gateway refused the key for alpha"}
        assert "fake-key" not in resp.text
    assert "fake-key" not in caplog.text and mux.agents["alpha"].turns == []


@pytest.mark.parametrize("agent, message, status, code", [
    ("gamma", "안녕", 404, "unknown_agent"),
    ("alpha", "   ", 400, "empty_message"),
    ("alpha", None, 400, "empty_message"),
    ("alpha", "가" * (lina_talk.MAX_CHARS + 1), 413, "message_too_long"),
])
def test_bad_input_answers_4xx_and_runs_nothing(work, mux, client, agent, message, status, code):
    resp = _talk(client, agent, message)
    assert resp.status_code == status and _valid("Error", resp.json())["error"]["code"] == code
    assert all(gw.calls == [] for gw in mux.agents.values())


def test_an_unknown_agent_has_no_history(work, mux, client):
    resp = client.get("/api/lina/agents/gamma/talk")
    assert resp.status_code == 404 and _valid("Error", resp.json())["error"]["code"] == "unknown_agent"
    assert all(gw.calls == [] for gw in mux.agents.values())


def test_a_busy_agent_answers_the_gateways_own_busy_response(work, mux, client):
    mux.agents["beta"].busy = True
    resp = _talk(client, "beta", "지금 돼?")
    assert resp.status_code == 429
    assert resp.json() == {"error": {"message": "Too many concurrent runs", "code": "rate_limited"}}
    assert mux.agents["beta"].turns == []


def test_the_to_do_talk_route_sends_the_owning_agents_own_key(work, mux, client):
    resp = client.post("/api/lina/work/items/T1/talk", json={"message": "색은 파랑으로 해"})
    assert resp.status_code == 200
    (turn,) = mux.agents["alpha"].turns
    assert turn["message"].endswith("\n\n색은 파랑으로 해") and mux.agents["beta"].calls == []


def test_both_routes_are_behind_the_dashboard_auth_gate(work, mux):
    from hermes_cli import web_server
    bare = TestClient(web_server.app)
    assert bare.post("/api/lina/agents/alpha/talk", json={"message": "안녕"}).status_code == 401
    assert bare.get("/api/lina/agents/alpha/talk").status_code == 401
    assert all(gw.calls == [] for gw in mux.agents.values())
