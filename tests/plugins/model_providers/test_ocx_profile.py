"""The ocx profile against a stub OCX: the only bundled provider, resolved, listed and called
over the OpenAI-compatible wire OCX serves (lina#7)."""

import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread

import pytest

from agent.transports.chat_completions import ChatCompletionsTransport
from providers import bundled_profiles, get_provider_profile, list_providers

STUB_KEY = "ocx-stub-key"
STUB_MODELS = ["openai/gpt-5.4", "anthropic/claude-sonnet-4.6"]


class _StubOcx(BaseHTTPRequestHandler):
    requests: list[dict] = []

    def _record(self, body: dict | None) -> None:
        type(self).requests.append({
            "method": self.command, "path": self.path, "body": body,
            "headers": {k.lower(): v for k, v in self.headers.items()},
        })

    def _send(self, status: int, payload: dict) -> None:
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _authorized(self) -> bool:
        return self.headers.get("Authorization") == f"Bearer {STUB_KEY}"

    def do_GET(self):
        self._record(None)
        if self.path != "/v1/models":
            return self._send(404, {"error": {"message": "not found"}})
        if not self._authorized():
            return self._send(401, {"error": {"message": "unauthorized"}})
        self._send(200, {"object": "list", "data": [{"id": m, "object": "model"} for m in STUB_MODELS]})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
        self._record(body)
        if self.path != "/v1/chat/completions":
            return self._send(404, {"error": {"message": "not found"}})
        if not self._authorized():
            return self._send(401, {"error": {"message": "unauthorized"}})
        self._send(200, {
            "id": "chatcmpl-stub", "object": "chat.completion", "created": 0, "model": body.get("model"),
            "choices": [{"index": 0, "finish_reason": "stop",
                         "message": {"role": "assistant", "content": "pong from stub ocx"}}],
            "usage": {"prompt_tokens": 1, "completion_tokens": 4, "total_tokens": 5},
        })

    def log_message(self, format, *args):
        return


@pytest.fixture
def stub_ocx(monkeypatch):
    _StubOcx.requests = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), _StubOcx)
    Thread(target=server.serve_forever, daemon=True).start()
    base_url = f"http://127.0.0.1:{server.server_address[1]}/v1"
    monkeypatch.setenv("OCX_API_KEY", STUB_KEY)
    monkeypatch.setenv("OCX_BASE_URL", base_url)
    try:
        yield base_url
    finally:
        server.shutdown()
        server.server_close()


def test_ocx_is_the_only_provider_profile():
    assert [p.name for p in bundled_profiles()] == ["ocx"]
    assert [p.name for p in list_providers()] == ["ocx"]
    profile = get_provider_profile("ocx")
    assert get_provider_profile("opencodex") is profile
    assert (profile.api_mode, profile.base_url) == ("chat_completions", "http://127.0.0.1:10100/v1")


def test_runtime_resolves_ocx_key_and_endpoint_from_env(stub_ocx):
    from hermes_cli.runtime_provider import resolve_runtime_provider

    runtime = resolve_runtime_provider(requested="ocx")
    assert runtime["provider"] == "ocx"
    assert runtime["api_mode"] == "chat_completions"
    assert runtime["api_key"] == STUB_KEY
    assert runtime["base_url"].rstrip("/") == stub_ocx


def test_fetch_models_lists_the_stub_catalog(stub_ocx):
    assert get_provider_profile("ocx").fetch_models(api_key=STUB_KEY, base_url=stub_ocx) == STUB_MODELS
    assert _StubOcx.requests[-1]["headers"]["authorization"] == f"Bearer {STUB_KEY}"


def test_chat_completion_reaches_stub_with_effort_and_session_affinity(stub_ocx):
    from openai import OpenAI

    kwargs = ChatCompletionsTransport().build_kwargs(
        model="openai/gpt-5.4", messages=[{"role": "user", "content": "ping"}], tools=None,
        provider_profile=get_provider_profile("ocx"),
        reasoning_config={"enabled": True, "effort": "ultra"}, session_id="cron_job42_20260801_090000",
    )
    response = OpenAI(api_key=STUB_KEY, base_url=stub_ocx).chat.completions.create(**kwargs)

    assert response.choices[0].message.content == "pong from stub ocx"
    sent = _StubOcx.requests[-1]
    assert sent["path"] == "/v1/chat/completions"
    assert sent["body"]["model"] == "openai/gpt-5.4"
    assert sent["body"]["reasoning_effort"] == "max"
    assert sent["headers"]["session-id"] == "cron_job42"


def test_disabled_reasoning_sends_none():
    _, top_level = get_provider_profile("ocx").build_api_kwargs_extras(
        reasoning_config={"enabled": False}, session_id=None,
    )
    assert top_level == {"reasoning_effort": "none"}
