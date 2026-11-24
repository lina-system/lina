"""Replay interrupted streams only before model output, using the provider's retry signals."""

import pytest

from tests.tui_gateway import _turn_host_parity_support as parity
from tests.tui_gateway.test_rust_host_model_retry import _turn
from tests.tui_gateway.test_turn_host_parity import _event, _host_frame

pytestmark = pytest.mark.platforms("linux")

ROLE = {"choices": [{"index": 0, "delta": {"role": "assistant"}}]}
PARTIAL = {"choices": [{"index": 0, "delta": {"content": "Partial answer"}}]}
QUOTA = {"code": "usage_limit_reached", "message": "Your account has reached its usage limit."}
BUSY = {"error": {"code": 429, "message": "temporarily busy"}}


@pytest.mark.parametrize(("first", "recovers"), [
    pytest.param(parity.Reply(disconnect="during_stream", sse_frames=(ROLE,)), True, id="role-only-reset"),
    pytest.param(parity.Reply(status=429, error_body={"error": {**QUOTA, "resets_in_seconds": 1}}),
                 True, id="quota-body-reset"),
    pytest.param(parity.Reply(status=429, error_body={"error": QUOTA, "resets_at": 1}),
                 True, id="quota-top-level-reset"),
    pytest.param(parity.Reply(status=429, error_body={"error": QUOTA}, reset_header="1"),
                 True, id="quota-header-reset"),
    pytest.param(parity.Reply(status=429, error_body={"error": QUOTA}), False, id="quota-without-reset"),
    pytest.param(parity.Reply(status=429, error_body={"error": {**QUOTA, "reset_at": "1" * 129}}),
                 False, id="quota-oversized-reset"),
    pytest.param(parity.Reply(sse_frames=(BUSY,)), True, id="sse-429-before-output"),
    pytest.param(parity.Reply(sse_frames=(PARTIAL, BUSY)), False, id="sse-429-after-output"),
    pytest.param(parity.Reply(sse_frames=({"choices": [{"delta": {"refusal": "Declined"}}]}, BUSY)),
                 False, id="sse-429-after-buffered-refusal"),
    pytest.param(parity.Reply(sse_frames=({"error": {"code": 400, "message": "bad request"}},)),
                 False, id="sse-400-before-output"),
])
def test_provider_failure_replay(first, recovers, tmp_path):
    with _turn(tmp_path / "rust", "rust", 3, (first, parity.Reply(text="Recovered."))) as (client, model):
        if PARTIAL in first.sse_frames:
            assert _event(client, "message.delta")["payload"]["text"] == "Partial answer"
        final = _event(client, "message.complete")["payload"]
        _host_frame(client, "turn.end", "turn.error")
        assert (len(model.requests), final["status"]) == ((2, "complete") if recovers else (1, "error"))
        if recovers:
            assert final["text"] == "Recovered."


def test_sse_server_error_uses_diagnostic_probe(tmp_path):
    first = parity.Reply(sse_frames=({"error": {"code": 503, "message": "unavailable"}},))
    replies = (first, parity.Reply(text="Recovered."))
    for implementation in ("python", "rust"):
        with _turn(tmp_path / implementation, implementation, 1, replies) as (client, model):
            final = _event(client, "message.complete")["payload"]
            _host_frame(client, "turn.end", "turn.error")
            assert [bool(request.get("stream")) for request in model.requests] == [True, False], implementation
            assert (final["status"], final["text"]) == ("complete", "Recovered.")
