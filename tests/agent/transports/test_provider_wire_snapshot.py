"""Shared request assembly for OCX routes and a profile-free compatible endpoint."""

from __future__ import annotations

import json
import os
import re
import socket
from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest

SNAPSHOT = Path(__file__).resolve().parents[2] / "fixtures" / "provider_wire_snapshot.json"

REASONING = {
    "unset": None,
    "high": {"enabled": True, "effort": "high"},
    "off": {"enabled": False},
}

TOOLS = [{"type": "function", "function": {
    "name": "terminal", "description": "Run a shell command.",
    "parameters": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}}}]

# One completed tool round, then a fresh user turn: role mapping, tool-call/result pairing
# and every cache breakpoint slot.
MESSAGES = [
    {"role": "system", "content": "You are a test agent."},
    {"role": "user", "content": "list files"},
    {"role": "assistant", "content": "", "tool_calls": [{"id": "call_1", "type": "function", "function": {
        "name": "terminal", "arguments": "{\"command\": \"ls\"}"}}]},
    {"role": "tool", "tool_call_id": "call_1", "content": "a.txt\nb.txt"},
    {"role": "user", "content": "thanks, now summarize"},
]

# Profile-free callers still use the kept host-inference tables.
BASE_URL_ONLY = {
    "base-url:openrouter": ("anthropic/claude-opus-5.5", "https://openrouter.ai/api/v1"),
    "base-url:nous": ("anthropic/claude-opus-5.5", "https://inference-api.nousresearch.com/v1"),
    "base-url:kimi": ("kimi-k3", "https://api.moonshot.ai/v1"),
    "base-url:kimi-coding": ("kimi-k3", "https://api.kimi.com/coding/v1"),
    "base-url:tokenhub": ("hunyuan-t1", "https://tokenhub.tencentmaas.com/v1"),
    "base-url:nvidia-nim": ("nvidia/nemotron-3-super", "https://integrate.api.nvidia.com/v1"),
    "base-url:qwen-portal": ("qwen3-coder-plus", "https://portal.qwen.ai/v1"),
    "base-url:lmstudio-port": ("qwen/qwen3-8b", "http://127.0.0.1:1234/v1"),
    "base-url:ollama": ("qwen3:8b", "http://127.0.0.1:11434/v1"),
    "base-url:generic": ("my-model", "https://llm.example.com/v1"),
}


# Values that legitimately differ per run or per release; everything else compares exactly.
_VOLATILE = (
    (re.compile(r"client=hermes-client-v[^\"]*"), "client=hermes-client-v<VERSION>"),
    (re.compile(r'"promptId": "[0-9a-f-]{36}"'), '"promptId": "<UUID>"'),
)


@pytest.fixture
def offline(monkeypatch):
    """No network and no ambient AWS profile. Capability probes (Ollama ``/api/show``, LM Studio,
    catalogs) must take their offline answer, or the recorded wire depends on a live server.
    Name resolution is blocked too: some clients connect through paths ``socket.connect`` misses."""
    real_connect = socket.socket.connect

    def guarded(sock, address, *args, **kwargs):
        if sock.family in (socket.AF_INET, socket.AF_INET6):
            raise OSError(f"network disabled in wire snapshot: {address}")
        return real_connect(sock, address, *args, **kwargs)

    def no_dns(*_args, **_kwargs):
        raise socket.gaierror("DNS disabled in wire snapshot")

    monkeypatch.setattr(socket.socket, "connect", guarded)
    monkeypatch.setattr(socket, "getaddrinfo", no_dns)
    monkeypatch.setenv("AWS_REGION", "us-east-1")
    monkeypatch.setenv("AWS_CONFIG_FILE", os.devnull)
    monkeypatch.setenv("AWS_SHARED_CREDENTIALS_FILE", os.devnull)


def _wire(provider: str, model: str, base_url: str) -> dict[str, Any]:
    from agent.turn_api_request import build_api_request
    from agent.turn_context import _PER_TURN_RESET_STATE
    from run_agent import AIAgent

    with patch("model_tools.get_tool_definitions", return_value=TOOLS), \
         patch("model_tools.check_toolset_requirements", return_value={}), \
         patch("agent.process_bootstrap.OpenAI"):
        agent = AIAgent(provider=provider, model=model, base_url=base_url, api_key="sk-test-0000000000",
                        quiet_mode=True, skip_context_files=True, skip_memory=True, session_id="wire-snapshot")
    for name, value in _PER_TURN_RESET_STATE:
        setattr(agent, name, value)
    out: dict[str, Any] = {"api_mode": agent.api_mode}
    for label, reasoning in REASONING.items():
        agent.reasoning_config = reasoning
        messages = json.loads(json.dumps(MESSAGES))
        built = build_api_request(
            agent, api_messages=messages, _moa_prepared_request=None, tools_for_api=agent.tools,
            system_message=messages[0]["content"], messages=messages,
            original_user_message=messages[-1]["content"], approx_tokens=0, total_chars=0, retry_count=0,
            api_call_count=1, api_request_id="req-1", api_start_time=0.0, effective_task_id="task-1",
            turn_id="turn-1",
        )
        text = json.dumps(built.api_kwargs, sort_keys=True, default=repr)
        for pattern, replacement in _VOLATILE:
            text = pattern.sub(replacement, text)
        out[label] = json.loads(text)
    return out


def _load() -> dict[str, Any]:
    return json.loads(SNAPSHOT.read_text(encoding="utf-8")) if SNAPSHOT.exists() else {}


@pytest.mark.parametrize("provider", ["ocx", "custom", "custom:relay"])
def test_provider_wire_matches_snapshot(offline, provider):
    """The remaining profile owns reasoning and affinity; history and tools survive assembly."""
    wire = _wire(provider, "relay-model", "http://127.0.0.1:10100/v1")
    assert wire["api_mode"] == "chat_completions"
    for label, effort in (("unset", "medium"), ("high", "high"), ("off", "none")):
        request = wire[label]
        assert request["model"] == "relay-model"
        assert request["messages"] == MESSAGES
        assert request["tools"] == TOOLS
        assert request.get("reasoning_effort") == effort
        assert request["extra_headers"] == {"session-id": "wire-snapshot"}
        assert "extra_body" not in request


@pytest.mark.parametrize("key", BASE_URL_ONLY)
def test_base_url_only_wire_matches_snapshot(offline, key):
    model, url = BASE_URL_ONLY[key]
    expected = _load()[key]
    assert _wire("", model, url) == {
        k: v for k, v in expected.items() if k not in ("model", "base_url")
    }
