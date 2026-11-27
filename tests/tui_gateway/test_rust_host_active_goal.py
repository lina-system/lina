"""Exercise active goals and their continuations through real compute-host routing."""

from __future__ import annotations

import json
from dataclasses import replace
from pathlib import Path
from threading import Event

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
FIRST_PROMPT = "Complete the first step."
GOAL_REPLIES = (
    Reply(text='{"verdict":"continue","reason":"One step remains."}'),
    Reply(text=SECOND),
    Reply(text='{"verdict":"done","reason":"Both steps are finished."}'),
)


def _configure_goal_profile(home: Path, workdir: Path, model_url: str) -> Path:
    served = home / "profiles" / "served"
    served.mkdir(parents=True)
    _config(home, workdir, model_url, "rust")
    _config(served, workdir, model_url, "rust")
    path = served / "config.yaml"
    config = json.loads(path.read_text(encoding="utf-8"))
    config["auxiliary"]["goal_judge"] = {
        "provider": "custom", "model": MODEL, "base_url": model_url, "api_key": MODEL_KEY,
    }
    path.write_text(json.dumps(config), encoding="utf-8")
    return served


@pytest.mark.parametrize("active", [True, False], ids=["active", "paused"])
def test_goal_continuation_routes_by_goal_state(
    workdir: Path, home: Path, rust_binary: Path, store_dsn: str, active: bool,
) -> None:
    replies = (Reply(text=FIRST), *GOAL_REPLIES) if active else (Reply(text=FIRST),)
    with loopback_model(replies) as model:
        served = _configure_goal_profile(home, workdir, model.url)
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


@pytest.mark.parametrize("resume", [False, True], ids=["set", "resume"])
def test_goal_activated_during_rust_turn_judges_reply_and_continues(
    workdir: Path, home: Path, rust_binary: Path, store_dsn: str, resume: bool,
) -> None:
    release = Event()
    continue_reply = Event()
    judge, continuation, final_judge = GOAL_REPLIES
    replies = (
        Reply(text=FIRST, gate=release), judge, replace(continuation, gate=continue_reply), final_judge,
    )
    with loopback_model(replies) as model:
        served = _configure_goal_profile(home, workdir, model.url)
        prewarm_env = {"PARITY_AGENT_PREWARM_DELAY": "600"}
        with gateway(home, rust_binary, prewarm_env, store_dsn, FIXTURE_SECRETS) as client:
            client.rpc("session.create", cwd=str(workdir), profile="served")
            if resume:
                client.rpc("command.dispatch", session_id=SID, name="goal", arg=f"set {OBJECTIVE}")
                client.rpc("command.dispatch", session_id=SID, name="goal", arg="pause")
            submission = client.rpc("prompt.submit", session_id=SID, text=FIRST_PROMPT)
            assert submission.get("turn_isolation"), submission
            try:
                model.wait_for_requests(1)
                _host_pid(served, "rust", rust_binary)
                if resume:
                    client.rpc("session.control", session_id=SID, action="goal.resume")
                else:
                    client.rpc("command.dispatch", session_id=SID, name="goal", arg=f"set {OBJECTIVE}")
                state = client.rpc("session.control.read", session_id=SID)["control"]["goal"]
                assert (state["status"], state["turns_used"]) == ("active", 0)
                after = len(client.sink.frames())
            finally:
                release.set()

            try:
                first = _event(client, "message.complete", after=after)["payload"]
                assert (first["status"], first["text"]) == ("complete", FIRST)
                boundary = client.wait(
                    lambda frame: frame.get("parity_host_frame", {}).get("type") in {"turn.end", "turn.error"}
                    or (frame.get("method") == "event" and frame["params"].get("session_id") == SID
                        and frame["params"]["type"] == "message.start"),
                    after=after,
                )
                assert boundary.get("parity_host_frame", {}).get("type") == "turn.end", boundary
                model.wait_for_requests(3)  # first answer, goal judge, continuation (still gated)
            finally:
                continue_reply.set()
            client.wait(
                lambda frame: frame.get("method") == "event"
                and frame["params"]["type"] == "session.control.update"
                and frame["params"]["payload"]["control"]["goal"]["status"] == "done",
            )
            state = client.rpc("session.control.read", session_id=SID)["control"]["goal"]
            assert (state["status"], state["turns_used"]) == ("done", 2)
            transcript = stored_rows(served)
            assert [row["content"] for row in transcript if row["role"] == "assistant"] == [FIRST, SECOND]
            prompts = [
                row["content"] for row in transcript
                if row["role"] == "user" and row.get("display_kind") != "model_switch"
            ]
            assert len(prompts) == 2 and prompts[0] == FIRST_PROMPT and OBJECTIVE in prompts[1]

        assert len(model.requests) == len(replies)
        assert FIRST in model.requests[1]["messages"][-1]["content"]
        assert not model.errors


@pytest.mark.parametrize("goal_state", ["inactive", "paused", "active"])
def test_failed_rust_turn_does_not_judge_goal(
    workdir: Path, home: Path, rust_binary: Path, store_dsn: str, goal_state: str,
) -> None:
    release = Event()
    with loopback_model((Reply(status=400, gate=release),)) as model:
        served = _configure_goal_profile(home, workdir, model.url)
        with gateway(home, rust_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
            client.rpc("session.create", cwd=str(workdir), profile="served")
            if goal_state == "paused":
                client.rpc("command.dispatch", session_id=SID, name="goal", arg=f"set {OBJECTIVE}")
                client.rpc("command.dispatch", session_id=SID, name="goal", arg="pause")
            submission = client.rpc("prompt.submit", session_id=SID, text=FIRST_PROMPT)
            assert submission.get("turn_isolation"), submission
            try:
                model.wait_for_requests(1)
                _host_pid(served, "rust", rust_binary)
                if goal_state == "active":
                    client.rpc("command.dispatch", session_id=SID, name="goal", arg=f"set {OBJECTIVE}")
            finally:
                release.set()

            assert _event(client, "message.complete")["payload"]["status"] == "error"
            _host_frame(client, "turn.end")
            state = client.rpc("session.control.read", session_id=SID)["control"]["goal"]
            if goal_state == "inactive":
                assert state is None
            else:
                assert (state["status"], state["turns_used"]) == (goal_state, 0)

        assert len(model.requests) == 1
        assert not model.errors
