"""Model output limits must not settle a native turn as a successful answer."""

from contextlib import closing
from pathlib import Path
from shutil import which

import pytest

from agent.turn_failure_copy import FAILED_TURN_DISPLAY_KIND
from hermes_state import SessionDB
from tests._fixtures.memory_store import APP_PASSWORD, PROCESS_STORE
from tests.tui_gateway import _turn_host_parity_support as parity
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_frame, _host_pid, _submit

FRAGMENT = "The answer starts here but cuts off mid-sentence"
FILE_CONTENT = "This read finished before the model hit its output limit."
PROMPT = "Read the fixture if needed, then answer."


@pytest.mark.platforms("linux")
@pytest.mark.parametrize("after_read", [False, True], ids=["first-round", "after-read"])
def test_rust_host_marks_length_stops_incomplete(
    tmp_path: Path, after_read: bool,
) -> None:
    binary = Path(which("lina-turn-host") or parity.ROOT / "target/debug/lina-turn-host")
    assert binary.is_file(), binary
    store_dsn = PROCESS_STORE.app_dsn()
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    (workspace / "fixture.txt").write_text(FILE_CONTENT, encoding="utf-8")
    home = tmp_path / "home"
    home.mkdir()
    read_call = {
        "id": "read-before-length",
        "type": "function",
        "function": {"name": "read_file", "arguments": '{"path": "fixture.txt"}'},
    }
    read_reply = parity.Reply(calls=(read_call,))
    length_reply = parity.Reply(
        text=FRAGMENT,
        finish_reason="length",
        usage={"prompt_tokens": 31, "completion_tokens": 11, "total_tokens": 42},
    )
    replies = (read_reply, length_reply) if after_read else (length_reply,)

    with parity.loopback_model(replies) as model:
        _config(home, workspace, model.url, "rust")
        with parity.gateway(home, binary, {}, store_dsn, (parity.MODEL_KEY, APP_PASSWORD)) as client:
            client.rpc("session.create", cwd=str(workspace))
            _submit(client, PROMPT)
            _host_pid(home, "rust", binary)
            final = _event(client, "message.complete")["payload"]
            _host_frame(client, "turn.end")
            rows = parity.stored_rows(home)
            with closing(SessionDB(home / "state.db", read_only=True)) as db:
                session = db.get_session(parity.SESSION_KEY)
            deltas = [
                frame["params"]["payload"]["text"] for frame in client.sink.frames()
                if frame.get("method") == "event" and frame["params"]["type"] == "message.delta"
            ]

    assert FRAGMENT in "".join(deltas), deltas
    assert final["status"] == "error", final
    assert final["failure_reason"] == "truncated", final
    assert final["recoverable"] is True
    assert "persisted_turn" not in final
    assert not model.errors, model.errors
    assert len(model.requests) == len(replies)
    assert model.requests[-1]["stream"] is True
    turn_start = next(
        index for index, row in enumerate(rows) if row["role"] == "user" and row["content"] == PROMPT
    )
    assert [row["role"] for row in rows[turn_start:]] == (
        ["user", "assistant", "tool", "assistant"] if after_read else ["user", "assistant"]
    )
    assert rows[-1]["display_kind"] == FAILED_TURN_DISPLAY_KIND
    error_surface = rows[-1]["display_metadata"]["error_surface"]
    assert (error_surface["code"], error_surface["retryable"]) == ("truncated", True)
    assert all(FRAGMENT not in (row.get("content") or "") for row in rows)
    if after_read:
        assert FILE_CONTENT in rows[-2]["content"]
        assert model.requests[-1]["messages"][-1]["tool_call_id"] == read_call["id"]
    prompt, completion = (51, 18) if after_read else (31, 11)
    assert (final["usage"]["calls"], final["usage"]["prompt"], final["usage"]["completion"]) == (
        len(replies), prompt, completion,
    )
    assert (session["input_tokens"], session["output_tokens"]) == (prompt, completion)
