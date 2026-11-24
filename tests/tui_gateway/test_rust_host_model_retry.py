"""Compare configured transient model retries through both real hosts."""

import json
import shutil
from contextlib import contextmanager
from pathlib import Path

import pytest

from tests._fixtures.memory_store import APP_PASSWORD, PROCESS_STORE
from tests.tui_gateway import _turn_host_parity_support as parity
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_frame, _host_pid, _submit

pytestmark = pytest.mark.platforms("linux")


@contextmanager
def _turn(home, implementation, configured, replies):
    binary = Path(shutil.which("lina-turn-host") or parity.ROOT / "target/debug/lina-turn-host")
    assert binary.is_file()
    home.mkdir()
    workspace = home / "workspace"
    workspace.mkdir()
    with parity.loopback_model(replies) as model:
        _config(home, workspace, model.url, implementation, max_turns=1)
        config_path = home / "config.yaml"
        config = json.loads(config_path.read_text(encoding="utf-8"))
        config["agent"]["api_max_retries"] = configured
        config_path.write_text(json.dumps(config), encoding="utf-8")
        try:
            with parity.gateway(home, binary, {}, PROCESS_STORE.app_dsn(), (parity.MODEL_KEY, APP_PASSWORD)) as client:
                client.rpc("session.create", cwd=str(workspace))
                _submit(client, "Retry the request.")
                _host_pid(home, implementation, binary)
                yield client, model
        finally:
            PROCESS_STORE.reset()
        assert not model.errors, model.errors


@pytest.mark.parametrize(
    ("configured", "status", "failures", "success"),
    [(0, 429, 1, False), (1, 429, 1, False), (3, 429, 2, True), (3, 429, 3, False),
     (3, 503, 3, True), (3, 503, 4, False), (3, 400, 1, False)],
)
def test_transient_failure_respects_attempt_budget(
    configured: int, status: int, failures: int, success: bool, tmp_path: Path,
) -> None:
    replies = (parity.Reply(status=status, retry_after="0.05"),) * failures + (parity.Reply(text="Recovered."),)
    # A streaming 5xx triggers one diagnostic POST without the stream option.
    expected_stream = [True] * (failures + int(success))
    if status == 503:
        expected_stream[1] = False
    for implementation in ("python", "rust"):
        with _turn(tmp_path / implementation, implementation, configured, replies) as (client, model):
            final = _event(client, "message.complete")["payload"]
            _host_frame(client, "turn.end", "turn.error")
            assert [bool(post.get("stream")) for post in model.requests] == expected_stream
            assert final["status"] == ("complete" if success else "error")
            if success:
                assert final["text"] == "Recovered."
            elif configured == 3 and status != 400:
                assert "3 attempts" in final["text"]


@pytest.mark.parametrize(
    ("first", "expected_calls", "status"),
    [(parity.Reply(disconnect="before_headers"), 2, "complete"),
     (parity.Reply(text="Partial answer", disconnect="during_stream"), 1, "error"),
     (parity.Reply(status=429, error_code="insufficient_quota"), 1, "error")],
)
def test_only_replay_safe_transport_failures(first, expected_calls, status, tmp_path):
    with _turn(tmp_path / "rust", "rust", 3, (first, parity.Reply(text="Recovered."))) as (client, model):
        if first.disconnect == "during_stream":
            assert _event(client, "message.delta")["payload"]["text"] == "Partial answer"
        assert _event(client, "message.complete")["payload"]["status"] == status
        _host_frame(client, "turn.end", "turn.error")
        assert len(model.requests) == expected_calls


def test_cancel_during_provider_backoff(tmp_path):
    with _turn(tmp_path / "rust", "rust", 3, (parity.Reply(status=429, retry_after="120"),)) as (client, model):
        assert _host_frame(client, "model.retry.ack")["retry_delay_s"] == 120
        assert client.rpc("session.interrupt", session_id=parity.SID)["status"] == "interrupted"
        assert _event(client, "message.complete")["payload"]["status"] == "interrupted"
        _host_frame(client, "turn.end", "turn.error")
        assert len(model.requests) == 1
