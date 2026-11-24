"""Drive both real compute hosts through empty reply recovery."""

import json
import os
import shutil
from contextlib import closing
from pathlib import Path

import pytest

from hermes_state import SessionDB
from hermes_state_store import TEXT_CACHE
from tests._fixtures.memory_store import APP_PASSWORD, PROCESS_STORE
from tests.tui_gateway import _turn_host_parity_support as parity
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_frame, _host_pid, _submit

ANSWER = "Recovered with a visible answer."
EMPTY = None
REASONING = {"reasoning_content": "Private intermediate reasoning."}
INLINE = {"content_chunks": ["<thi", "nk>Private intermediate reasoning.</thi", "nk>"]}
TOOL_CALL = {
    "id": "call-empty-parity",
    "type": "function",
    "function": {"name": "read_file", "arguments": '{"path": "fixture.txt"}'},
}
PROMPTS = ("First request.", "Second request.", "Check the transcript.")


def _prompt(messages: list[parity.Json]) -> str:
    return next(
        prompt for row in reversed(messages) if row["role"] == "user"
        for prompt in reversed(PROMPTS) if prompt in row["content"]
    )


def assert_empty_recovery_parity(
    case_name: str,
    attempts: tuple[parity.Json | None, ...],
    limit: int,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binary = Path(shutil.which("lina-turn-host") or parity.ROOT / "target/debug/lina-turn-host")
    assert binary.is_file(), binary
    store_dsn = PROCESS_STORE.app_dsn()
    monkeypatch.setattr(parity, "TIMEOUT", 65)

    def complete(handler: parity.ModelHandler, request: parity.Json, index: int, _reply: parity.Reply) -> None:
        user = _prompt(request["messages"])
        count = sum(
            _prompt(previous["messages"]) == user
            for previous in handler.server.requests[:index]
        )
        result = {"content": ANSWER} if user == PROMPTS[-1] else attempts[min(count, len(attempts) - 1)]
        envelope = {"id": f"local-{index}", "created": 1, "model": parity.MODEL}
        message = {"role": "assistant", "content": None, **(result or {})}
        if "tool_calls" in message and user == PROMPTS[1]:
            message["tool_calls"] = [{
                **TOOL_CALL, "function": {**TOOL_CALL["function"], "arguments": '{"path": "second.txt"}'},
            }]
        content_chunks = message.pop("content_chunks", [])
        if content_chunks:
            message["content"] = "".join(content_chunks)
        finish = "tool_calls" if "tool_calls" in message else "stop"
        output = 7 if result else 0
        usage = {"prompt_tokens": 20, "completion_tokens": output, "total_tokens": 20 + output}
        if request.get("stream"):
            if "tool_calls" in message:
                message["tool_calls"] = [{"index": 0, **call} for call in message["tool_calls"]]
            deltas = [{**message, "content": chunk} for chunk in content_chunks] if content_chunks else [message]
            chunks = [
                *({"choices": [{"index": 0, "delta": delta}]} for delta in deltas),
                {"choices": [{"index": 0, "delta": {}, "finish_reason": finish}]},
                {"choices": [], "usage": usage},
            ]
            body = "".join(f"data: {json.dumps({**envelope, **chunk})}\n\n" for chunk in chunks)
            handler._send(200, "text/event-stream", (body + "data: [DONE]\n\n").encode())
        else:
            body = {**envelope, "choices": [{"index": 0, "message": message, "finish_reason": finish}],
                    "usage": usage}
            handler._send(200, "application/json", json.dumps(body).encode())

    monkeypatch.setattr(parity.ModelHandler, "_complete", complete)
    observations = {}
    for implementation in ("python", "rust"):
        home = tmp_path / implementation
        home.mkdir()
        workspace = home / "workspace"
        workspace.mkdir()
        (workspace / "fixture.txt").write_text("A real tool result.\n", encoding="utf-8")
        (workspace / "second.txt").write_text("Another tool result.\n", encoding="utf-8")
        with parity.loopback_model((parity.Reply(),) * (2 * limit + 1)) as model:
            _config(home, workspace, model.url, implementation)
            with parity.gateway(home, binary, {}, store_dsn, (parity.MODEL_KEY, APP_PASSWORD)) as client:
                client.rpc("session.create", cwd=str(workspace))
                finals = []
                for prompt in PROMPTS:
                    after = len(client.sink.frames())
                    _submit(client, prompt)
                    if not finals:
                        _host_pid(home, implementation, binary)
                    final = _event(client, "message.complete", after=after)["payload"]
                    _host_frame(client, "turn.end", "turn.error", after=after)
                    finals.append({key: final.get(key) for key in ("status", "text", "reasoning")})
                rows = [
                    {key: row.get(key) for key in ("role", "content", "reasoning", "reasoning_content")}
                    for row in parity.stored_rows(home)
                ]
                with closing(SessionDB(home / "state.db", read_only=True)) as db:
                    session = db.get_session(parity.SESSION_KEY)
                    stored_usage = {key: session[key] for key in ("input_tokens", "output_tokens")}
                visible_deltas = [
                    frame["params"]["payload"]["text"] for frame in client.sink.frames()
                    if frame.get("method") == "event" and frame["params"]["type"] == "message.delta"
                ]
                assert "Private intermediate reasoning" not in "".join(visible_deltas)
            assert not model.errors, model.errors
            requests = [request["messages"][1:] for request in model.requests]
        observations[implementation] = {
            "finals": finals, "rows": rows, "requests": requests, "stored_usage": stored_usage,
        }
        PROCESS_STORE.reset()
        TEXT_CACHE.clear()

    if path := os.environ.get("HERMES_E2E_ARTIFACTS"):
        output = Path(path) / f"{case_name}.json"
        output.write_text(json.dumps(observations, indent=2) + "\n", encoding="utf-8")

    python = observations["python"]
    assert len(python["requests"]) == 2 * limit + 1
    assert python["stored_usage"]["input_tokens"] == 20 * (2 * limit + 1)
    assert observations["rust"] == python
