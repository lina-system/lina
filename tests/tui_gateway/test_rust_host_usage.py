"""Compare real compute-host accounting in the served profile's state.db."""

from __future__ import annotations

import json
import os
import signal
from contextlib import closing
from pathlib import Path
from threading import Event

import pytest

from hermes_state import SessionDB
from tests.tui_gateway._turn_host_parity_support import (
    MODEL,
    SESSION_KEY,
    SID,
    ModelHandler,
    Reply,
    gateway,
    loopback_model,
)
from tests.tui_gateway.test_rust_host_rollback import FIXTURE_SECRETS, rust_binary, store_dsn, workdir
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_pid, _tool_call

__all__ = ["rust_binary", "store_dsn", "workdir"]

pytestmark = pytest.mark.platforms("linux")

TOOL_USAGE = {
    "prompt_tokens": 1000, "completion_tokens": 70, "total_tokens": 1070,
    "prompt_tokens_details": {"cached_tokens": 100, "cache_creation_input_tokens": 50},
    "completion_tokens_details": {"reasoning_tokens": 17},
}
STORED_FIELDS = ("input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens")


def _serve_prices(handler: ModelHandler) -> None:
    if handler.path != "/v1/models":
        handler.send_error(404)
        return
    models = {"data": [{"id": MODEL, "pricing": {
        "prompt": "0.00001", "completion": "0.00002", "input_cache_read": "0.000001",
        "cache_creation": "0.000015", "request": "0.001",
    }}]}
    handler._send(200, "application/json", json.dumps(models).encode())


def _usage_config(home: Path, workspace: Path, url: str, impl: str) -> None:
    _config(home, workspace, url, impl)
    path = home / "config.yaml"
    settings = json.loads(path.read_text(encoding="utf-8"))
    settings["model"].update(provider="custom", base_url=url)
    settings.pop("custom_providers")
    path.write_text(json.dumps(settings), encoding="utf-8")


def test_native_tool_turn_settles_usage_once_in_served_profile(
    workdir: Path, rust_binary: Path, store_dsn: str, monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(ModelHandler, "do_GET", _serve_prices, raising=False)
    (workdir / "sample.txt").write_text("Read-only fixture.\n", encoding="utf-8")
    replies = (
        Reply(calls=(_tool_call(1, "sample.txt"),), usage=TOOL_USAGE),
        Reply(text="Done.", usage={
            "prompt_tokens": 1200, "completion_tokens": 30, "total_tokens": 1230,
            "cache_read_input_tokens": 200, "cache_write_tokens": 10,
            "completion_tokens_details": {"reasoning_tokens": 3},
        }),
    )
    usage_fields = ("input", "output", "reasoning", "prompt", "completion", "total", "calls", "cache_hit_pct")
    for impl in ("python", "rust"):
        launch = workdir / f"{impl}-launch"
        served = launch / "profiles" / "served"
        served.mkdir(parents=True)
        with loopback_model(replies) as model:
            _usage_config(launch, workdir, model.url, impl)
            _usage_config(served, workdir, model.url, impl)
            with gateway(launch, rust_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
                assert client.rpc("session.create", cwd=str(workdir), profile="served")["session_id"] == SID
                submission = client.rpc("prompt.submit", session_id=SID, text="Read the file.")
                if impl == "rust":
                    assert submission.get("turn_isolation"), submission
                    _host_pid(served, impl, rust_binary)
                complete = _event(client, "message.complete")["payload"]
                assert complete["status"] == "complete"
                assert complete["text"] == "Done."
                assert tuple(complete["usage"][key] for key in usage_fields) == (
                    1840, 100, 20, 2200, 100, 2300, 2, 14,
                )
            assert len(model.requests) == 2
            assert not model.errors
            with closing(SessionDB(served / "state.db", read_only=True)) as db:
                row = db.get_session(SESSION_KEY)
                assert row is not None
                assert tuple(row[key] for key in STORED_FIELDS) == (1840, 100, 300, 60, 20)
                assert row["estimated_cost_usd"] == pytest.approx(0.0236)
                assert (row["cost_status"], row["cost_source"]) == ("estimated", "provider_models_api")
                assert db.get_recent_session_model_route(SESSION_KEY) == {
                    "model": MODEL, "billing_provider": "custom", "billing_base_url": model.url,
                    "billing_mode": "", "api_call_count": 2,
                }


def test_native_crash_settles_the_preflight_response_only(
    workdir: Path, rust_binary: Path, store_dsn: str, monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(ModelHandler, "do_GET", _serve_prices, raising=False)
    (workdir / "sample.txt").write_text("Read-only fixture.\n", encoding="utf-8")
    home = workdir / "home"
    home.mkdir()
    replies = (
        Reply(calls=(_tool_call(1, "sample.txt"),), usage=TOOL_USAGE),
        Reply(text="Never delivered.", gate=Event()),
    )
    with loopback_model(replies) as model:
        _usage_config(home, workdir, model.url, "rust")
        with gateway(home, rust_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
            assert client.rpc("session.create", cwd=str(workdir))["session_id"] == SID
            assert client.rpc("prompt.submit", session_id=SID, text="Read the file.")["turn_isolation"]
            pid = _host_pid(home, "rust", rust_binary)
            model.wait_for_requests(2)  # The parent has accepted the first sample at preflight.
            os.kill(pid, signal.SIGKILL)
            complete = _event(client, "message.complete")["payload"]
            assert complete["status"] == "error"
            with closing(SessionDB(home / "state.db", read_only=True)) as db:
                row = db.get_session(SESSION_KEY)
                assert row is not None
                assert tuple(row[key] for key in STORED_FIELDS) == (850, 70, 100, 50, 17)
                assert row["estimated_cost_usd"] == pytest.approx(0.01175)
                route = db.get_recent_session_model_route(SESSION_KEY)
                assert route is not None and route["api_call_count"] == 1
