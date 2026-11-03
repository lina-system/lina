"""``POST /api/lina/work/items/{item_id}/talk`` (lina#167) over a scratch LINA root: a work record in the sqlite store,
agent profile homes under the scratch root, and a fake gateway (the session list/create and chat-stream routes of
``gateway/platforms/api_server.py``, in their shapes) whose reply comes from a fake model. The stream is validated
against ``docs/api/talk-client.yaml``; the agent's next Telegram DM turn is ``TurnRunner._prepare_turn_message``.
"""

from __future__ import annotations

import json
import time
from pathlib import Path
from types import SimpleNamespace

import httpx
import jsonschema
import pytest
from fastapi.testclient import TestClient

import hermes_yaml
from hermes_cli import lina_ocx, lina_talk
from tests.hermes_cli._work_env import WorkEnv

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]


def _openapi_to_jsonschema(node):
    if isinstance(node, list):
        return [_openapi_to_jsonschema(n) for n in node]
    if not isinstance(node, dict):
        return node
    out = {k: _openapi_to_jsonschema(v) for k, v in node.items() if k != "nullable"}
    if node.get("nullable") and "type" in out:
        out["type"] = [out["type"], "null"]
    return out


_COMPONENTS = _openapi_to_jsonschema(
    hermes_yaml.safe_load((REPO / "docs/api/talk-client.yaml").read_text(encoding="utf-8"))["components"])


def _valid(schema_name: str, data):
    jsonschema.Draft7Validator({"$ref": f"#/components/schemas/{schema_name}", "components": _COMPONENTS}).validate(data)
    return data


def fake_model(text: str) -> str:
    return "알겠어요 — " + text.splitlines()[-1]


class FakeGateway:
    """One agent's gateway api_server: ``/p/<agent>/api/sessions`` (list by title, create) and the session chat
    stream, answering the turn with :func:`fake_model`. ``busy`` answers the stream with the gateway's 429."""

    KEY = "gw-key"

    def __init__(self):
        self.sessions: dict[str, dict] = {}
        self.turns: list[dict] = []
        self.calls: list[str] = []
        self.busy = False

    def handle(self, request: httpx.Request) -> httpx.Response:
        self.calls.append(f"{request.method} {request.url.path}")
        if request.headers.get("authorization") != f"Bearer {self.KEY}":
            return httpx.Response(401, json={"error": {"message": "Invalid API key"}})
        path = request.url.path
        if request.method == "GET" and path.endswith("/api/sessions"):
            title = request.url.params.get("title")
            return httpx.Response(200, json={"data": [s for s in self.sessions.values() if s["title"] == title]})
        if request.method == "POST" and path.endswith("/api/sessions"):
            body = json.loads(request.content)
            sid = f"api_{len(self.sessions) + 1}"
            self.sessions[sid] = {"id": sid, "title": body["title"], "source": body["source"]}
            return httpx.Response(201, json={"object": "hermes.session", "session": self.sessions[sid]})
        if request.method == "POST" and path.endswith("/chat/stream"):
            if self.busy:
                return httpx.Response(429, json={"error": {"message": "Too many concurrent runs", "code": "rate_limited"}})
            sid = path.split("/")[-3]
            body = json.loads(request.content)
            self.turns.append({"session_id": sid, **body})
            return httpx.Response(200, headers={"content-type": "text/event-stream"},
                                  content=self._stream(sid, body["message"]))
        return httpx.Response(404, json={"error": {"message": "not found"}})

    @staticmethod
    def _stream(sid: str, message: str) -> bytes:
        reply, seq = fake_model(message), [0]

        def frame(name, payload):
            seq[0] += 1
            payload = {**payload, "session_id": sid, "run_id": "run_1", "seq": seq[0], "ts": time.time()}
            return f"event: {name}\ndata: {json.dumps(payload, ensure_ascii=False)}\n\n"
        return "".join([
            frame("run.started", {"user_message": {"role": "user", "content": message}, "runtime": {}}),
            frame("message.started", {"message": {"id": "msg_1", "role": "assistant"}}),
            ": keepalive\n\n",
            frame("assistant.delta", {"message_id": "msg_1", "delta": reply[:3]}),
            frame("assistant.delta", {"message_id": "msg_1", "delta": reply[3:]}),
            frame("assistant.completed", {"message_id": "msg_1", "content": reply, "runtime": {}}),
            frame("run.completed", {"message_id": "msg_1", "messages": [], "usage": {}, "runtime": {}}),
            frame("done", {}),
        ]).encode()


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: env.root)
    env.goal_and_todos()
    env.ok("add", "할 일", "--by", "owner", "--from", "beta", "--title", "회의 자료 보기", "--why", "기록용",
           "--done", "확인했다")
    config = env.root / "config.yaml"
    agents = "agents: {alpha: 알파, beta: 베타}"
    config.write_text(config.read_text(encoding="utf-8").replace(agents, agents[:-1] + ", gamma: 감마}"), encoding="utf-8")
    env.ok("add", "할 일", "--by", "owner", "--from", "gamma", "--title", "감마가 맡긴 일", "--why", "기록용",
           "--done", "확인했다")
    env.config()  # gamma retired: T3 answers to no agent now
    return env


@pytest.fixture
def gateway(work, monkeypatch):
    gw = FakeGateway()
    homes = {name: work.root / "profiles" / name for name in ("alpha", "beta")}
    for name, home in homes.items():  # each agent's own Telegram home channel: the owner's DM with it
        home.mkdir(parents=True)
        (home / "config.yaml").write_text(
            "platforms:\n  telegram:\n    home_channel: {platform: telegram, chat_id: '42', name: Owner}\n",
            encoding="utf-8")
    monkeypatch.setattr(lina_talk, "profile_home", lambda agent: homes[agent])
    monkeypatch.setattr(lina_talk, "gateway", lambda agent: (f"http://127.0.0.1:8642/p/{agent}", FakeGateway.KEY))
    monkeypatch.setattr(lina_talk, "http_client", lambda: httpx.AsyncClient(transport=httpx.MockTransport(gw.handle)))
    gw.homes = homes
    return gw


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
        frames.append(_valid("TalkEvent", {"event": event, "data": data}))
    return frames


def _talk(client, item: str, message):
    return client.post(f"/api/lina/work/items/{item}/talk", json={"message": message})


def _telegram_turn(home: Path, message: str, chat_id: str = "42") -> str:
    """The model-facing message of the agent's next Telegram DM turn in *chat_id*; the stored row is checked to be
    the typed text whatever the model reads."""
    from gateway.config import GatewayConfig, Platform
    from gateway.run_turn_runner import TurnRunner
    from gateway.session import SessionSource
    source = SessionSource(platform=Platform.TELEGRAM, user_id=chat_id, chat_id=chat_id, chat_type="dm")
    runner = SimpleNamespace(_resolve_profile_home_for_source=lambda src: home, config=GatewayConfig(),
                             session_store=SimpleNamespace(_entries={}))
    ctx = SimpleNamespace(source=source, message=message, persist_user_message=None, persist_user_timestamp=None,
                          inbound_message_id=None, history=[], session_key=f"agent:main:telegram:dm:{chat_id}")
    persist, _ts = TurnRunner(runner, ctx)._prepare_turn_message([])
    assert persist in (None, message)
    return ctx.message


def test_a_message_streams_the_reply_from_the_owning_agents_app_conversation(work, gateway, client):
    resp = _talk(client, "T1", "  색은 파랑으로 해  ")
    assert resp.status_code == 200 and resp.headers["content-type"].startswith("text/event-stream")
    frames = _frames(resp.text)
    names = [f["event"] for f in frames]
    assert names == ["talk.started", "run.started", "message.started", "assistant.delta", "assistant.delta",
                     "assistant.completed", "talk.carried", "run.completed", "done"]
    started = frames[0]["data"]
    assert started["item"] == {"id": "T1", "title": "기기 1 점검"} and started["agent"] == "alpha"
    reply = "".join(f["data"]["delta"] for f in frames if f["event"] == "assistant.delta")
    assert reply == fake_model("색은 파랑으로 해") == frames[5]["data"]["content"]
    assert gateway.sessions[started["session_id"]]["title"] == "LINA app: alpha"
    assert all(call.split()[1].startswith("/p/alpha/") for call in gateway.calls)


def test_the_turn_is_the_owners_from_the_app_and_names_the_to_do(work, gateway, client):
    _talk(client, "T2", "자료는 어디 있어?")
    (turn,) = gateway.turns
    assert turn["author"] == {"id": "owner", "name": "주인", "is_bot": False}
    assert turn["message"] == '[LINA app · to-do T2 "회의 자료 보기"]\n\n자료는 어디 있어?'
    assert gateway.sessions[turn["session_id"]]["title"] == "LINA app: beta"  # the owner's to-do: the asking agent


def test_the_same_agent_keeps_one_app_conversation(work, gateway, client):
    _talk(client, "T1", "하나")
    _talk(client, "G1", "둘")
    assert len(gateway.sessions) == 1 and {t["session_id"] for t in gateway.turns} == set(gateway.sessions)


def test_the_agents_next_telegram_turn_has_what_was_said(work, gateway, client):
    _talk(client, "T1", "색은 파랑으로 해")
    assert _telegram_turn(gateway.homes["alpha"], "누구세요", chat_id="7") == "누구세요"  # not the owner's chat
    seen = _telegram_turn(gateway.homes["alpha"], "아까 말한 거 했어?")
    assert seen.endswith("\n\n아까 말한 거 했어?")
    assert '"기기 1 점검"' in seen and "Owner: 색은 파랑으로 해" in seen
    assert f"You replied: {fake_model('색은 파랑으로 해')}" in seen
    assert _telegram_turn(gateway.homes["alpha"], "또") == "또"  # read once
    assert _telegram_turn(gateway.homes["beta"], "베타") == "베타"  # another agent's turn sees nothing


@pytest.mark.parametrize("item, message, status, code", [
    ("T99", "안녕", 404, "unknown_item"),
    ("T3", "안녕", 409, "no_owning_agent"),
    ("T1", "   ", 400, "empty_message"),
    ("T1", None, 400, "empty_message"),
    ("T1", "가" * (lina_talk.MAX_CHARS + 1), 413, "message_too_long"),
])
def test_bad_input_answers_4xx_and_runs_nothing(work, gateway, client, item, message, status, code):
    resp = _talk(client, item, message)
    assert resp.status_code == status
    assert _valid("Error", resp.json())["error"]["code"] == code
    assert gateway.calls == [] and not (gateway.homes["alpha"] / "app_talk").exists()


def test_a_busy_agent_answers_the_gateways_own_busy_response(work, gateway, client):
    gateway.busy = True
    resp = _talk(client, "T1", "지금 돼?")
    assert resp.status_code == 429
    assert resp.json() == {"error": {"message": "Too many concurrent runs", "code": "rate_limited"}}
    assert gateway.turns == [] and _telegram_turn(gateway.homes["alpha"], "x") == "x"


def test_the_route_is_behind_the_dashboard_auth_gate(work, gateway):
    from hermes_cli import web_server
    resp = TestClient(web_server.app).post("/api/lina/work/items/T1/talk", json={"message": "안녕"})
    assert resp.status_code == 401 and gateway.calls == []
