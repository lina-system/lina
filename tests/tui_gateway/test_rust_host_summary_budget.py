"""Check iteration summary admission against the observed model attempts."""

from contextlib import closing
from pathlib import Path

import pytest

from agent.context_compressor import MAX_ITERATIONS_SUMMARY_REQUEST
from hermes_state import SessionDB
from tests._fixtures.memory_store import APP_PASSWORD, PROCESS_STORE
from tests.tui_gateway import _turn_host_parity_support as parity
from tests.tui_gateway import test_rust_turn_plan as plan_tests
from tests.tui_gateway.test_turn_host_parity import (
    _config,
    _event,
    _host_frame,
    _host_pid,
    _submit,
    _tool_call,
)
from tui_gateway.contracts.compute_host import ContextPreflight
from tui_gateway.rust_turn_plan import (
    RustTurnConflict,
    prepare_context_preflight,
    prepare_empty_retry,
    prepare_iteration_summary,
    prepare_rust_turn,
    rust_turn_lease,
)

pytestmark = pytest.mark.platforms("linux")
admitted_turn = plan_tests.turn


def test_empty_recovery_spends_an_iteration_before_native_summary(tmp_path: Path) -> None:
    binary = parity.ROOT / "target/debug/lina-turn-host"
    assert binary.is_file(), binary
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    (workspace / "fixture.txt").write_text("A result worth summarizing.\n", encoding="utf-8")
    tool_call = _tool_call(1, "fixture.txt")
    answer = "I read the fixture and found the requested result."
    replies = (
        parity.Reply(usage={"prompt_tokens": 20, "completion_tokens": 0, "total_tokens": 20}),
        parity.Reply(calls=(tool_call,)),
        parity.Reply(text=answer),
    )

    with parity.loopback_model(replies) as model:
        _config(tmp_path, workspace, model.url, "rust", max_turns=2)
        with parity.gateway(tmp_path, binary, {}, PROCESS_STORE.app_dsn(),
                            (parity.MODEL_KEY, APP_PASSWORD)) as client:
            client.rpc("session.create", cwd=str(workspace))
            _submit(client, "Read the fixture and tell me what you found.")
            _host_pid(tmp_path, "rust", binary)
            final = _event(client, "message.complete")["payload"]
            terminal = _host_frame(client, "turn.end", "turn.error")

        assert not model.errors, model.errors
        request_count = len(model.requests)
        assert request_count == 3, (final, terminal)
        summary = model.requests[-1]
        assert summary["messages"][-1] == {"role": "user", "content": MAX_ITERATIONS_SUMMARY_REQUEST}
        assert [row["tool_call_id"] for row in summary["messages"] if row["role"] == "tool"] == [
            tool_call["id"],
        ]

    assert final["status"] == "complete"
    assert final["text"] == answer
    rows = parity.stored_rows(tmp_path)
    assert [(row["role"], row["content"]) for row in rows[-2:]] == [
        ("user", MAX_ITERATIONS_SUMMARY_REQUEST),
        ("assistant", answer),
    ]


def test_empty_final_attempt_settles_completed_tool_and_usage(tmp_path: Path) -> None:
    binary = parity.ROOT / "target/debug/lina-turn-host"
    assert binary.is_file(), binary
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    file_text = "A completed read survives the final empty response."
    (workspace / "fixture.txt").write_text(f"{file_text}\n", encoding="utf-8")
    tool_call = _tool_call(1, "fixture.txt")
    replies = (
        parity.Reply(calls=(tool_call,), usage={"prompt_tokens": 20, "completion_tokens": 7, "total_tokens": 27}),
        parity.Reply(usage={"prompt_tokens": 30, "completion_tokens": 0, "total_tokens": 30}),
    )

    with parity.loopback_model(replies) as model:
        _config(tmp_path, workspace, model.url, "rust", max_turns=2)
        with parity.gateway(tmp_path, binary, {}, PROCESS_STORE.app_dsn(),
                            (parity.MODEL_KEY, APP_PASSWORD)) as client:
            client.rpc("session.create", cwd=str(workspace))
            _submit(client, "Read the fixture and tell me what you found.")
            _host_pid(tmp_path, "rust", binary)
            final = _event(client, "message.complete")["payload"]
            _host_frame(client, "turn.end", "turn.error")

        assert not model.errors, model.errors
        assert len(model.requests) == 2
        assert model.requests[-1]["messages"][-1]["tool_call_id"] == tool_call["id"]

    assert final["status"] == "complete"
    assert final["text"]
    assert {key: final["usage"][key] for key in ("calls", "input", "output")} == {
        "calls": 2, "input": 50, "output": 7,
    }
    rows = parity.stored_rows(tmp_path)
    tool_row, empty_row = rows[-2:]
    assert (tool_row["role"], tool_row["tool_call_id"]) == ("tool", tool_call["id"])
    assert file_text in tool_row["content"]
    assert (empty_row["role"], empty_row["content"]) == ("assistant", "(empty)")
    with closing(SessionDB(tmp_path / "state.db", read_only=True)) as db:
        stored = db.get_session(parity.SESSION_KEY)
    assert (stored["input_tokens"], stored["output_tokens"]) == (50, 7)


@pytest.mark.parametrize("last_response_empty", [False, True], ids=["under-budget", "last-response-empty"])
def test_summary_requires_budget_exhaustion_on_a_tool_round(admitted_turn, last_response_empty: bool) -> None:
    session, frame, _ = admitted_turn
    session["agent"].max_iterations = 2
    session["agent"].compression_enabled = False
    assert prepare_rust_turn("runtime", session, frame) is not None
    messages = plan_tests._tool_result_frame(frame)["messages"]
    preflight = ContextPreflight.model_validate({
        "type": "context.preflight", "sid": "runtime", "turn_id": frame["turn_id"],
        "request_id": "first-tool-round", "priced_prefix_count": len(frame["history"]) + 1,
        "response_index": 1, "messages": messages, "summary": False, "usage": None,
    })
    unaccounted_call = _tool_call(2, "fixture.txt")
    premature = {
        "turn_id": frame["turn_id"],
        "messages": [
            *messages,
            {"role": "assistant", "content": "", "tool_calls": [unaccounted_call]},
            {"role": "tool", "content": "second result", "tool_call_id": unaccounted_call["id"]},
        ],
    }

    with rust_turn_lease(session, frame):
        assert prepare_context_preflight(session, preflight) == "allow"
        if last_response_empty:
            empty = ContextPreflight.model_validate({
                **preflight.model_dump(),
                "request_id": "empty-follow-up", "priced_prefix_count": len(messages),
                "response_index": 2, "empty_retry": True,
            })
            assert prepare_empty_retry(session, empty) == 0.0
            premature["messages"] = messages
        with pytest.raises(RustTurnConflict, match="summary does not follow the admitted iteration budget"):
            prepare_iteration_summary(session, premature)
