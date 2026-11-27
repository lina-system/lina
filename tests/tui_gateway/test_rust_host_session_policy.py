"""Session policy changes must reach the next already-warm native turn."""

import json
from pathlib import Path

import pytest

from tests.tui_gateway._parity_network_guard import build_network_guard, guarded_arm
from tests.tui_gateway._turn_host_parity_support import MODEL_KEY, SID, Reply, gateway, loopback_model
from tests.tui_gateway.test_rust_host_rollback import FIXTURE_SECRETS, rust_binary, store_dsn
from tests.tui_gateway.test_rust_turn_plan import _configure_cold_turn, _done, turn
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_frame, _host_pid, _submit

__all__ = ["rust_binary", "store_dsn", "turn"]
pytestmark = pytest.mark.platforms("linux")


def test_session_reasoning_changes_next_rust_request(tmp_path: Path, rust_binary: Path, store_dsn: str):
    home = tmp_path / "home"
    home.mkdir()
    (home / "models_dev_cache.json").write_text(json.dumps({"magpie": {"models": {}}}))
    with loopback_model((Reply(text="First answer."), Reply(text="Second answer."))) as model:
        _config(home, tmp_path, model.url, "rust")
        path = home / "config.yaml"
        cfg = json.loads(path.read_text())
        cfg["model"].update(provider="magpie", base_url=model.url)
        cfg["agent"]["reasoning_effort"] = "low"
        path.write_text(json.dumps(cfg))
        env = {"MAGPIE_API_KEY": MODEL_KEY, "MAGPIE_BASE_URL": model.url}
        with (
            guarded_arm(build_network_guard(tmp_path), home, env) as guarded,
            gateway(home, rust_binary, guarded, store_dsn, FIXTURE_SECRETS) as client,
        ):
            client.rpc("session.create", cwd=str(tmp_path))
            _submit(client, "First request.")
            _host_frame(client, "turn.end")
            pid = _host_pid(home, "rust", rust_binary)
            assert model.requests[0]["reasoning_effort"] == "low"

            result = client.rpc("config.set", session_id=SID, scope="session", key="reasoning", value="high")
            assert result["value"] == "high"
            assert client.rpc("config.get", key="reasoning")["value"] == "low"
            after = len(client.sink.frames())
            _submit(client, "Second request.")
            assert _event(client, "message.complete", after=after)["payload"]["text"] == "Second answer."
            _host_frame(client, "turn.end", after=after)
            assert _host_pid(home, "rust", rust_binary) == pid
            assert not any(frame.get("parity_parent_agent_built") for frame in client.sink.frames())
            assert [body["reasoning_effort"] for body in model.requests] == ["low", "high"]


@pytest.mark.parametrize("eligible", [True, False], ids=["first-party", "proxy"])
def test_session_fast_uses_actual_route_and_next_native_plan(turn, monkeypatch, eligible):
    import providers
    from providers import ProviderProfile
    from tui_gateway import server
    from tui_gateway.rust_turn_plan import persist_rust_turn, prepare_rust_turn, rust_turn_lease

    session, frame, _ = turn
    session["agent"] = None
    _configure_cold_turn(session, monkeypatch)
    # The native chat host supports a system message. GPT-5 uses a developer message.
    model = "gpt-4.1"
    url = "https://api.openai.com/v1" if eligible else "http://127.0.0.1:9/v1"
    home = Path(session["profile_home"])
    (home / "models_dev_cache.json").write_text(json.dumps({"openai": {"models": {}}}))
    config_path = home / "config.yaml"
    cfg = json.loads(config_path.read_text())
    cfg["model"].update(default=model, provider="openai", base_url=url)
    config_path.write_text(json.dumps(cfg))
    profile = ProviderProfile(name="openai")
    lookup = providers.get_provider_profile
    monkeypatch.setattr(providers, "get_provider_profile", lambda name: profile if name == "openai" else lookup(name))
    session["model_override"] = {"model": model, "provider": "openai"}
    # Supply only the resolved transport. Both billing eligibility and request assembly are real.
    runtime = {"provider": "openai", "base_url": url, "api_key": MODEL_KEY, "api_mode": "chat_completions"}
    monkeypatch.setattr(server, "_resolve_agent_model_runtime", lambda *_: (model, runtime.copy()))
    monkeypatch.setitem(server._sessions, frame["sid"], session)

    def set_fast(value):
        return server._methods["config.set"]("rpc", {
            "session_id": frame["sid"], "scope": "session", "key": "fast", "value": value,
        })

    def plan_next(value):
        session["running"] = False
        assert set_fast(value)["result"]["value"] == value
        session["running"] = True
        frame.update(request_id=value, turn_id=value, text=value, history_version=session["history_version"])
        return prepare_rust_turn(frame["sid"], session, frame)

    prepared = prepare_rust_turn(frame["sid"], session, frame)
    assert prepared is not None, "initial chat route must support native planning"
    first, _ = prepared
    assert (first["model"], first["base_url"]) == (model, url)
    assert "service_tier" not in first["params"]
    with rust_turn_lease(session, frame):
        persist_rust_turn(session, _done(frame))

    if eligible:
        fast, _ = plan_next("fast")
        assert fast["params"].get("service_tier") == "priority"
        with rust_turn_lease(session, frame):
            persist_rust_turn(session, _done(frame))
        normal, _ = plan_next("normal")
        assert "service_tier" not in normal["params"]
        with rust_turn_lease(session, frame):
            persist_rust_turn(session, _done(frame))
    else:
        session["running"] = False
        response = set_fast("fast")
        assert response.get("error", {}).get("code") == 4002, response
    assert plan_next("auto") is None
    assert session["agent"] is None
