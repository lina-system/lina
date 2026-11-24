"""Exercise refusal handling through real compute-host pipes and loopback HTTP."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Final

import pytest

from tests.tui_gateway._turn_host_parity_support import (
    MODEL,
    SID,
    Json,
    ModelHandler,
    Reply,
    gateway,
    loopback_model,
    stored_rows,
)
from tests.tui_gateway.test_rust_host_rollback import FIXTURE_SECRETS, rust_binary, store_dsn, workdir
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_frame, _host_pid, _submit

__all__ = ["rust_binary", "store_dsn", "workdir"]

pytestmark = pytest.mark.platforms("linux")

PROMPT: Final = "Please answer this request."
EXPLANATION: Final = "I cannot help with that request."
VISIBLE: Final = "This part of the answer is available."


@pytest.mark.parametrize("implementation", ["python", "rust"])
@pytest.mark.parametrize(
    ("stream", "finish_reason", "content", "refusal", "terminal"),
    [
        pytest.param(False, "stop", None, EXPLANATION, True, id="message-stop"),
        pytest.param(False, None, None, EXPLANATION, True, id="message-null"),
        pytest.param(True, "stop", None, EXPLANATION, True, id="delta-stop"),
        pytest.param(True, None, None, EXPLANATION, True, id="delta-null"),
        pytest.param(False, "stop", VISIBLE, EXPLANATION, False, id="message-visible"),
        pytest.param(True, "stop", VISIBLE, EXPLANATION, False, id="delta-visible"),
        pytest.param(False, "content_filter", None, None, True, id="message-filter"),
        pytest.param(True, "content_filter", VISIBLE, EXPLANATION, True, id="delta-filter-visible"),
    ],
)
def test_provider_refusal_respects_the_finish_signal(
    workdir: Path,
    rust_binary: Path,
    store_dsn: str,
    monkeypatch: pytest.MonkeyPatch,
    implementation: str,
    stream: bool,
    finish_reason: str | None,
    content: str | None,
    refusal: str | None,
    terminal: bool,
) -> None:
    original_complete = ModelHandler._complete

    def complete(handler: ModelHandler, request: Json, index: int, reply: Reply) -> None:
        if index:
            original_complete(handler, request, index, reply)
            return
        envelope = {"id": "refusal-response", "created": 1, "model": MODEL}
        usage = {"prompt_tokens": 20, "completion_tokens": 7, "total_tokens": 27}
        if request.get("stream"):
            deltas: list[Json] = [{"role": "assistant", "content": content}]
            if refusal:
                deltas.extend([{"refusal": refusal[:8]}, {"refusal": refusal[8:]}])
            chunks = [
                *({"choices": [{"index": 0, "delta": delta, "finish_reason": None}]} for delta in deltas),
                {"choices": [{"index": 0, "delta": {}, "finish_reason": finish_reason}]},
                {"choices": [], "usage": usage},
            ]
            data = "".join(f"data: {json.dumps({**envelope, **chunk})}\n\n" for chunk in chunks)
            handler._send(200, "text/event-stream", (data + "data: [DONE]\n\n").encode())
        else:
            message = {"role": "assistant", "content": content, "refusal": refusal}
            result = {**envelope, "usage": usage, "choices": [
                {"index": 0, "message": message, "finish_reason": finish_reason},
            ]}
            handler._send(200, "application/json", json.dumps(result).encode())

    monkeypatch.setattr(ModelHandler, "_complete", complete)
    home = workdir / "home"
    home.mkdir()
    with loopback_model((Reply(), Reply(text="The model was called again."))) as model:
        _config(home, workdir, model.url, implementation)
        config_path = home / "config.yaml"
        config = json.loads(config_path.read_text(encoding="utf-8"))
        config["model"]["streaming"] = stream
        config_path.write_text(json.dumps(config), encoding="utf-8")
        with gateway(home, rust_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
            client.rpc("session.create", cwd=str(workdir))
            _submit(client, PROMPT)
            _host_pid(home, implementation, rust_binary)
            final = _event(client, "message.complete")["payload"]
            end = _host_frame(client, "turn.end", "turn.error")
            assert end["type"] == "turn.end", (end, final)
            deltas = [
                frame["params"]["payload"]["text"] for frame in client.sink.frames()
                if frame.get("method") == "event" and frame["params"]["type"] == "message.delta"
                and frame["params"].get("session_id") == SID
            ]
        requests = [bool(request.get("stream")) for request in model.requests]
        assert not model.errors
    rows = stored_rows(home)
    prompt_at = next(i for i, row in enumerate(rows) if row["role"] == "user" and row["content"] == PROMPT)
    user, assistant = rows[prompt_at:]

    assert requests == [stream], (requests, final)
    assert EXPLANATION not in "".join(deltas)
    assert user["content"] == PROMPT
    assert assistant["role"] == "assistant"
    if terminal:
        assert final["status"] == "error", final
        assert "safety filter" in final["text"], final
        assert (content or refusal or "the model returned no explanation") in final["text"], final
        assert final["error_surface"]["code"] == "content_policy_blocked", final
        assert final["error_surface"]["retryable"] is False, final
        assert assistant["display_kind"] == "failed_turn"
        assert assistant["display_metadata"]["error_surface"]["code"] == "content_policy_blocked"
        assert assistant["display_metadata"]["error_surface"]["retryable"] is False
        assert EXPLANATION not in assistant["content"]
        assert VISIBLE not in assistant["content"]
    else:
        assert final["status"] == "complete", final
        assert final["text"] == VISIBLE
        assert assistant["content"] == VISIBLE
        assert assistant.get("display_kind") != "failed_turn"
