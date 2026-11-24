"""Exercise active goals and their continuations through real compute-host routing."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from tests.tui_gateway._turn_host_parity_support import (
    MODEL, MODEL_KEY, SID, Reply, gateway, loopback_model, stored_rows,
)
from tests.tui_gateway.test_rust_host_rollback import FIXTURE_SECRETS, home, rust_binary, store_dsn, workdir
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_frame, _host_pid

__all__ = ["home", "rust_binary", "store_dsn", "workdir"]

pytestmark = pytest.mark.platforms("linux")

FIRST = "I finished the first step."
SECOND = "I finished the remaining work."
OBJECTIVE = "Finish two steps."


@pytest.mark.parametrize("active", [True, False], ids=["active", "paused"])
def test_goal_continuation_routes_by_goal_state(
    workdir: Path, home: Path, rust_binary: Path, store_dsn: str, active: bool,
) -> None:
    served = home / "profiles" / "served"
    served.mkdir(parents=True)
    replies = (Reply(text=FIRST),)
    if active:
        replies += (
            Reply(text='{"verdict":"continue","reason":"One step remains."}'),
            Reply(text=SECOND),
            Reply(text='{"verdict":"done","reason":"Both steps are finished."}'),
        )
    with loopback_model(replies) as model:
        _config(home, workdir, model.url, "rust")
        _config(served, workdir, model.url, "rust")
        path = served / "config.yaml"
        config = json.loads(path.read_text(encoding="utf-8"))
        config["auxiliary"]["goal_judge"] = {
            "provider": "custom", "model": MODEL, "base_url": model.url, "api_key": MODEL_KEY,
        }
        path.write_text(json.dumps(config), encoding="utf-8")

        with gateway(home, rust_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
            client.rpc("session.create", cwd=str(workdir), profile="served")
            goal = client.rpc("command.dispatch", session_id=SID, name="goal", arg=f"set {OBJECTIVE}")
            if not active:
                client.rpc("command.dispatch", session_id=SID, name="goal", arg="pause")
            submission = client.rpc("prompt.submit", session_id=SID, text=goal["message"])
            first = _event(client, "message.complete")["payload"]
            assert first["status"] == "complete" and first["text"] == FIRST, first
            if active:
                assert not (served / "state" / f"dashboard-rust-host-{SID}.json").exists()
                client.wait(
                    lambda frame: frame.get("method") == "event"
                    and frame["params"]["type"] == "session.control.update"
                    and frame["params"]["payload"]["control"]["goal"]["status"] == "done",
                )
                if submission.get("turn_isolation"):
                    _host_pid(home, "python", rust_binary)
                    _host_frame(client, "turn.end")
            else:
                assert submission.get("turn_isolation"), submission
                _host_pid(served, "rust", rust_binary)
                _host_frame(client, "turn.end")

            completions = [
                frame["params"]["payload"]["text"]
                for frame in client.sink.frames()
                if frame.get("method") == "event" and frame["params"]["type"] == "message.complete"
            ]
            expected = [FIRST, SECOND] if active else [FIRST]
            assert completions == expected
            state = client.rpc("session.control.read", session_id=SID)["control"]["goal"]
            assert (state["status"], state["turns_used"]) == (("done", 2) if active else ("paused", 0))
            transcript = stored_rows(served)
            assert [row["content"] for row in transcript if row["role"] == "assistant"] == expected
            if active:
                users = [row["content"] for row in transcript if row["role"] == "user"]
                assert len(users) == 2 and OBJECTIVE in users[1]
        assert len(model.requests) == len(replies)
        assert not model.errors
