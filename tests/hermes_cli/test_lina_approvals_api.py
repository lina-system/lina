"""Signed-in approval routing through the real dashboard (lina#878)."""

import asyncio
import hashlib
import json
import logging
import sqlite3
import time
from contextlib import closing
from types import SimpleNamespace
from unittest.mock import MagicMock

import httpx
import pytest
from aiohttp import web
from aiohttp.test_utils import TestClient as GatewayClient, TestServer
from fastapi.testclient import TestClient

from gateway.config import PlatformConfig
from gateway.owner_identity import host_owner_subject
from gateway.platforms.api_server import APIServerAdapter
from gateway.platforms.api_server_run_idempotency import RunIdempotencyStore
from hermes_cli import lina_deps, lina_talk, web_server, web_server_cron
from hermes_cli.dashboard_auth import clear_providers, register_provider
from tests.hermes_cli._work_env import NOW, WorkEnv
from tests.hermes_cli.conftest_dashboard_auth import StubAuthProvider
from tests.hermes_cli.test_dashboard_auth_middleware import _complete_stub_login
from tools import approval as approval_mod, approval_context
from tools.approval_gateway_wait import _ApprovalEntry, _await_gateway_decision

pytestmark = pytest.mark.platforms("posix")

_PATH = "/api/lina/approvals"
_DECISION = {"decision": "approve", "content_version": "a" * 64}


def _approval(agent: str) -> dict[str, str | float]:
    return {
        "request_id": f"req_{agent}",
        "run_id": f"run_{agent}",
        "target": f"{agent} tool",
        "action_summary": f"Review {agent} action",
        "content_version": _DECISION["content_version"],
        "expires_at": NOW + 300.0,
        "state": "pending",
    }


class Gateways:
    def __init__(self) -> None:
        self.queues = {agent: [_approval(agent)] for agent in ("alpha", "beta")}
        self.status = {"alpha": 200, "beta": 200}
        self.post_status = 200
        self.drop_ack = False
        self.posts: list[tuple[str, str, dict[str, str]]] = []

    def handle(self, request: httpx.Request) -> httpx.Response:
        _, prefix, agent, *route = request.url.path.split("/")
        assert prefix == "p"
        assert request.headers["Authorization"] == f"Bearer fake-key-{agent}"
        if request.method == "GET":
            assert route == ["v1", "approvals"]
            return httpx.Response(self.status[agent], json={"approvals": self.queues[agent]})

        assert request.method == "POST"
        assert route == ["v1", "runs", f"run_{agent}", "approval"]
        body = json.loads(request.content)
        self.posts.append((agent, route[2], body))
        if self.post_status != 200:
            return httpx.Response(self.post_status)
        self.queues[agent].clear()
        if self.drop_ack:
            raise httpx.ReadError("response lost", request=request)
        return httpx.Response(200, json={"request_id": body["request_id"], "run_id": route[2],
                                        "choice": body["choice"], "resolved": 1})


@pytest.fixture
def work(tmp_path, monkeypatch) -> WorkEnv:
    config = WorkEnv(tmp_path, monkeypatch, locale="en")
    monkeypatch.setattr(lina_deps, "lina_root", lambda: config.root)
    return config


@pytest.fixture
def gateways(work, monkeypatch) -> Gateways:
    gateways = Gateways()
    for agent in gateways.queues:
        monkeypatch.setenv(f"LINA_GATEWAY_KEY_{agent.upper()}", f"fake-key-{agent}")
    monkeypatch.setattr(lina_talk, "profile_home", lambda agent: work.root / "profiles" / agent)
    monkeypatch.setattr(web_server_cron, "_gateway_fire_endpoint",
                        lambda agent, home: f"http://127.0.0.1:8642/p/{agent}/api/cron/fire")
    monkeypatch.setattr(lina_talk, "http_client",
                        lambda: httpx.AsyncClient(transport=httpx.MockTransport(gateways.handle)))
    return gateways


@pytest.fixture
def client(work, monkeypatch):
    monkeypatch.setattr(web_server.app.state, "auth_required", True, raising=False)
    monkeypatch.setattr(web_server.app.state, "bound_host", "dashboard.example.test", raising=False)
    monkeypatch.setattr(web_server.app.state, "bound_port", 443, raising=False)
    clear_providers()
    register_provider(StubAuthProvider())
    try:
        client = TestClient(web_server.app, base_url="https://dashboard.example.test")
        _complete_stub_login(client)
        yield client
    finally:
        clear_providers()


def test_lists_configured_agents_without_exposing_run_ids(client, gateways):
    response = client.get(_PATH)
    assert response.status_code == 200
    assert response.json() == {
        "approvals": [{"agent": agent, **{k: v for k, v in rows[0].items() if k != "run_id"}}
                      for agent, rows in gateways.queues.items()],
    }
    gateways.status["alpha"] = 501
    response = client.get(_PATH)
    assert response.status_code == 200
    assert [row["agent"] for row in response.json()["approvals"]] == ["beta"]
    gateways.status["beta"] = 501
    assert client.get(_PATH).status_code == 501
    assert client.post(f"{_PATH}/req_beta", json=_DECISION).status_code == 501
    assert not gateways.posts


@pytest.mark.parametrize(("biometric", "decision", "choice"), [
    (None, "approve", "once"),
    ("off", "approve", "once"),
    ("off", "reject", "deny"),
    ("true", "approve", "once"),
])
def test_resolves_once_as_verified_session_using_button(client, work, gateways, biometric, decision, choice):
    if biometric is not None:
        work.config(locale="en", extra=f"approvals: {{biometric: {biometric}}}\n")
    path = f"{_PATH}/req_beta"
    body = {**_DECISION, "decision": decision}
    response = client.post(path, json=body)
    assert response.status_code == 200
    assert response.json()["resolved"] == 1
    assert client.post(path, json=body).status_code == 404
    assert gateways.posts == [("beta", "run_beta", {
        "request_id": "req_beta", "choice": choice, "content_version": _DECISION["content_version"],
        "button_subject": host_owner_subject(), "check_method": "button",
    })]


@pytest.mark.parametrize(("fault", "status"), [
    ("changed", 409), ("duplicate", 409), ("unavailable", 502),
    ("invalid_decision", 422), ("nonstring_version", 422), ("malformed_version", 422), ("forged_subject", 422),
])
def test_refuses_invalid_or_unsafe_routes_without_submitting(client, gateways, fault, status):
    body = dict(_DECISION)
    if fault == "changed":
        body["content_version"] = "b" * 64
    if fault == "duplicate":
        gateways.queues["alpha"][0]["request_id"] = "req_beta"
    if fault == "unavailable":
        gateways.status["alpha"] = 503
    if fault == "invalid_decision":
        body["decision"] = "once"
    if fault == "nonstring_version":
        body["content_version"] = 123
    if fault == "malformed_version":
        body["content_version"] = "wrong"
    if fault == "forged_subject":
        body["button_subject"] = "forged"
    assert client.post(f"{_PATH}/req_beta", json=body).status_code == status
    assert not gateways.posts


def test_requires_signed_session_even_with_valid_loopback_token(gateways, monkeypatch):
    monkeypatch.setattr(web_server.app.state, "auth_required", False, raising=False)
    client = TestClient(web_server.app)
    client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    assert client.get(_PATH).status_code == 401
    assert client.post(f"{_PATH}/req_beta", json=_DECISION).status_code == 401
    assert not gateways.posts


def test_gateway_refusal_after_lookup_is_not_reported_as_success(client, gateways):
    gateways.post_status = 409
    assert client.post(f"{_PATH}/req_beta", json=_DECISION).status_code == 409
    assert len(gateways.posts) == 1
    assert len(gateways.queues["beta"]) == 1


def test_dropped_confirmation_never_retries_the_decision(client, gateways):
    gateways.drop_ack = True
    path = f"{_PATH}/req_beta"
    assert client.post(path, json=_DECISION).status_code == 502
    assert client.post(path, json=_DECISION).status_code == 404
    assert len(gateways.posts) == 1


@pytest.mark.asyncio
@pytest.mark.parametrize(("choice", "stale", "decision"), [
    ("once", False, "approve"), ("deny", False, "reject"), ("once", True, None),
])
async def test_gateway_button_approval_audits_only_a_winning_version(tmp_path, caplog, choice, stale, decision):
    run_id, request_id, subject = "run_audit", "approval_audit", "uid:1234"
    command = "synthetic action text"
    version = hashlib.sha256(command.encode()).hexdigest()
    entry = _ApprovalEntry({
        "request_id": request_id, "command": command, "content_version": version, "expires_at": time.time() + 60,
    })
    adapter = APIServerAdapter(PlatformConfig(enabled=True, extra={"key": "fake-gateway-key"}))
    adapter._run_owners[run_id] = adapter._run_idempotency_scope(SimpleNamespace(headers={}))
    adapter._set_run_status(run_id, "waiting_for_approval")
    adapter._run_approval_sessions[run_id] = run_id
    app = web.Application()
    for method, path, handler in adapter._http_route_table():
        app.router.add_route(method, path, handler)

    audit_logger = logging.getLogger("gateway.platforms.api_server")
    log_path = tmp_path / "gateway.log"
    with closing(logging.FileHandler(log_path, encoding="utf-8")) as log_handler:
        audit_logger.addHandler(log_handler)
        with approval_mod._lock:
            approval_mod._gateway_queues[run_id] = [entry]
        try:
            with caplog.at_level(logging.INFO, logger=audit_logger.name):
                async with GatewayClient(TestServer(app)) as cli:
                    response = await cli.post(
                        f"/v1/runs/{run_id}/approval", headers={"Authorization": "Bearer fake-gateway-key"}, json={
                            "request_id": request_id, "choice": choice, "button_subject": subject,
                            "check_method": "button",
                            "content_version": hashlib.sha256(b"previous action").hexdigest() if stale else version,
                        },
                    )
                    log_text = await asyncio.to_thread(log_path.read_text, encoding="utf-8")
        finally:
            audit_logger.removeHandler(log_handler)
            with approval_mod._lock:
                approval_mod._gateway_queues.pop(run_id, None)
            await adapter.disconnect()

    assert response.status == (409 if stale else 200)
    assert entry.result == (None if stale else choice)
    records = [json.loads(line.removeprefix("approval.button ")) for line in log_text.splitlines()
               if line.startswith("approval.button ")]
    assert command not in log_text
    if decision is None:
        assert records == []
    else:
        record, = records
        assert record.pop("timestamp") > 0
        assert record == {"check_method": "button", "content_version": version, "decision": decision,
                          "request_id": request_id, "subject": subject}


@pytest.mark.asyncio
async def test_idempotent_run_keeps_legacy_approval_shape_in_sqlite(tmp_path, monkeypatch):
    approval = {
        "request_id": "approval-persisted-shape",
        "command": "synthetic action",
        "description": "synthetic description",
        "pattern_key": "synthetic",
        "allow_session": False,
        "allow_permanent": False,
    }
    version = hashlib.sha256(approval["command"].encode()).hexdigest()
    monkeypatch.setattr(approval_context, "_get_approval_timeout", lambda: 15)

    def run_conversation(**_):
        key = approval_context.get_current_session_key()
        _await_gateway_decision(key, approval_mod._gateway_notify_cb(key), {**approval, "content_version": version})
        return {"final_response": "done"}

    def read_persisted_status(run_id):
        with closing(sqlite3.connect(db_path)) as connection:
            row = connection.execute("SELECT status_json FROM run_idempotency WHERE run_id = ?", (run_id,)).fetchone()
        return json.loads(row[0])

    async def read_approval_event(events):
        async for line in events.content:
            if line.startswith(b"data: "):
                event = json.loads(line.removeprefix(b"data: "))
                if event["event"] == "approval.request":
                    return event
        pytest.fail("Run stream ended before the approval request")

    adapter = APIServerAdapter(PlatformConfig(enabled=True, extra={"key": "fake-gateway-key"}))
    db_path = tmp_path / "runs.sqlite"
    adapter._run_idempotency_store.close()
    adapter._run_idempotency_store = RunIdempotencyStore(str(db_path))
    agent = MagicMock()
    agent.run_conversation.side_effect = run_conversation
    agent.session_prompt_tokens = agent.session_completion_tokens = agent.session_total_tokens = 0
    monkeypatch.setattr(adapter, "_create_agent", lambda **_: agent)
    app = web.Application()
    for method, path, handler in adapter._http_route_table():
        app.router.add_route(method, path, handler)

    run_id = None
    try:
        auth = {"Authorization": "Bearer fake-gateway-key"}
        async with GatewayClient(TestServer(app), headers=auth) as cli:
            async with cli.post("/v1/runs", json={"input": "Synthetic input"},
                                headers={"Idempotency-Key": "approval-shape"}) as response:
                assert response.status == 202
                run_id = (await response.json())["run_id"]
            async with cli.get(f"/v1/runs/{run_id}/events") as events:
                assert events.status == 200
                event = await asyncio.wait_for(read_approval_event(events), timeout=5)
            pending, = approval_mod.list_gateway_approvals(run_id)
            persisted = await asyncio.to_thread(read_persisted_status, run_id)

        assert pending["request_id"] == event["request_id"] == approval["request_id"]
        assert pending["content_version"] == event["content_version"] == version
        assert pending["expires_at"] == event["expires_at"] > event["timestamp"]
        assert persisted["status"] == "waiting_for_approval"
        assert persisted["approval"] == {
            **approval,
            "event": "approval.request",
            "run_id": run_id,
            "timestamp": event["timestamp"],
            "choices": ["once", "deny"],
            "timeout_s": 15,
        }
    finally:
        try:
            if run_id is not None:
                approval_mod.unregister_gateway_notify(run_id)
                task = adapter._active_run_tasks.get(run_id)
                if task is not None:
                    await asyncio.wait_for(asyncio.shield(task), timeout=5)
        finally:
            await adapter.disconnect()
