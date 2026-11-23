"""Functional contracts for the Python-to-Rust turn preparation boundary."""

from __future__ import annotations

import copy
import threading
from contextlib import contextmanager
from pathlib import Path
from types import SimpleNamespace

import pytest

from tui_gateway.rust_turn_plan import (
    RustTurnConflict,
    persist_rust_turn,
    prepare_rust_turn,
    rust_turn_lease,
)

pytestmark = pytest.mark.platforms("linux")


@pytest.fixture
def turn(tmp_path, monkeypatch):
    """Use a real agent, assembly, SQLite leases and the local fixture store."""
    import httpx
    from openai import OpenAI

    from agent import agent_init
    from agent.context_compressor import ContextCompressor
    from hermes_state import SessionDB
    from run_agent import AIAgent
    from tools.environments.local import LocalEnvironment
    from tools.file_tools import READ_FILE_SCHEMA
    from tui_gateway import server

    home = tmp_path / "profile"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setattr(server, "_launch_home", lambda: home)
    monkeypatch.setattr(server, "_session_home", lambda session: home)
    monkeypatch.setattr(server, "_session_cwd", lambda session: str(tmp_path))
    monkeypatch.setattr(server, "_set_session_context", lambda *a, **k: [])
    monkeypatch.setattr(server, "_clear_session_context", lambda tokens: None)
    for name in (
        "_wire_callbacks", "_apply_pending_model_switch", "_sync_agent_model_with_config",
        "_sync_agent_compression_with_config", "_sync_agent_fallback_with_config",
        "_sync_bot_capabilities",
    ):
        monkeypatch.setattr(server, name, lambda *args: None)
    monkeypatch.setenv("OPENAI_API_KEY", "launch-only-must-not-escape")
    monkeypatch.setenv("UNRELATED_SECRET", "another-launch-secret")

    @contextmanager
    def profile_scope(session, **kwargs):
        from agent.secret_scope import reset_secret_scope, set_secret_scope
        from hermes_constants import reset_hermes_home_override, set_hermes_home_override

        h = set_hermes_home_override(str(home))
        s = set_secret_scope({"CUSTOM_API_KEY": "scoped-test-key"}, profile_home=str(home))
        try:
            yield
        finally:
            reset_secret_scope(s)
            reset_hermes_home_override(h)

    monkeypatch.setattr(server, "_session_profile_runtime_scope", profile_scope)

    def no_network(request):
        raise AssertionError("test attempted model/network access")

    http = httpx.Client(transport=httpx.MockTransport(no_network))
    client = OpenAI(api_key="scoped-test-key", base_url="https://model.example/v1", http_client=http)
    db = SessionDB(db_path=home / "state.db")
    db.create_session("stored", source="tui", model="test-model")

    def load_tools(agent, *args):
        agent.tools = [{"type": "function", "function": copy.deepcopy(READ_FILE_SCHEMA)}]
        agent.valid_tool_names = {"read_file"}
        agent._configured_disabled_toolsets = None

    def build_client(agent, *args):
        agent.client = client
        agent.api_key = "scoped-test-key"
        agent.base_url = "https://model.example/v1"
        agent._client_kwargs = {
            "api_key": agent.api_key, "base_url": agent.base_url,
        }

    monkeypatch.setattr(agent_init, "_load_tools", load_tools)
    monkeypatch.setattr(agent_init, "_build_client", build_client)
    # Model metadata and plugin resolution are outside this boundary; use the
    # real built-in compressor without an endpoint metadata probe.
    def context_engine(agent, *args):
        agent.context_compressor = ContextCompressor(model="test-model", config_context_length=100000)
        agent._context_engine = None

    monkeypatch.setattr(agent_init, "_build_context_engine", context_engine)
    agent = AIAgent(
        model="test-model", provider="custom", api_mode="chat_completions",
        api_key="scoped-test-key", base_url="https://model.example/v1",
        session_id="stored", session_db=db, quiet_mode=True,
        skip_context_files=True, skip_memory=True, checkpoints_enabled=False,
        reasoning_config={"enabled": False},
    )
    # A stored prompt must be replayed byte-for-byte, with the actual ephemeral
    # prompt appended by production request assembly.
    agent._cached_system_prompt = "Stored system prompt"
    agent.ephemeral_system_prompt = "Session instructions"
    agent._use_prompt_caching = False
    local = LocalEnvironment.__new__(LocalEnvironment)
    monkeypatch.setattr("tools.file_tools._get_file_ops", lambda key: SimpleNamespace(env=local))
    session = {
        "agent": agent, "session_key": "stored", "profile_home": str(home),
        "history": [], "history_version": 0, "history_lock": threading.Lock(),
        "running": True, "cwd": str(tmp_path),
    }
    frame = {
        "type": "turn.start", "sid": "runtime", "session_key": "stored",
        "request_id": "request", "turn_id": "turn", "history_version": 0,
        "history": [], "text": "Read the file", "cwd": str(tmp_path),
    }
    monkeypatch.setattr(server, "_ensure_session_db_row", lambda session: True)
    monkeypatch.setattr(server, "_reopen_if_finalized", lambda store, key: None)

    @contextmanager
    def session_db(session):
        yield db

    monkeypatch.setattr(server, "_session_db", session_db)
    try:
        yield session, frame, db
    finally:
        agent.close()
        db.close()
        client.close()


def test_plan_uses_actual_prompt_tools_parameters_and_scoped_auth(turn):
    session, frame, _ = turn
    agent = session["agent"]
    agent.request_overrides = {"temperature": 0.37, "max_tokens": 321}

    plan, env = prepare_rust_turn("runtime", session, frame)

    assert env == {"OPENAI_API_KEY": "scoped-test-key"}
    assert plan["system"] == "Stored system prompt\n\nSession instructions"
    assert plan["tools"] == agent.tools
    assert plan["params"]["temperature"] == 0.37
    assert plan["params"]["max_tokens"] == 321
    assert plan["model"] == agent.model
    assert plan["base_url"] == agent.base_url
    assert plan["stream"] is True
    assert plan["params"]["stream_options"] == {"include_usage": True}
    assert plan["params"]["timeout"] == agent._resolved_api_call_timeout()
    assert frame["terminal_backend"] == "local"


def test_missing_procfs_stays_on_python_before_agent_preparation(turn, monkeypatch):
    from tui_gateway import server

    session, frame, _ = turn
    session["agent"] = None
    original = Path.is_dir
    monkeypatch.setattr(
        Path, "is_dir", lambda path: False if str(path) == "/proc/self/fd" else original(path),
    )

    def forbidden(*args):
        raise AssertionError("agent preparation ran without procfs")

    monkeypatch.setattr(server, "_start_agent_build", forbidden)

    assert prepare_rust_turn("runtime", session, frame) is None
    assert "_rust_turn_stage" not in session


@pytest.mark.parametrize("headers,eligible", [
    ({"session-id": "stored"}, True),
    ({"session-id": "foreign-session"}, False),
    ({"Authorization": "synthetic-token"}, False),
    ({"x-opencode-session": "stored\r\nInjected: value"}, False),
])
def test_affinity_headers_require_trusted_conversation_value(turn, monkeypatch, headers, eligible):
    from agent import chat_completion_helpers

    session, frame, _ = turn
    original = chat_completion_helpers.build_api_kwargs

    def with_headers(*args, **kwargs):
        result = original(*args, **kwargs)
        result["extra_headers"] = headers
        return result

    monkeypatch.setattr(chat_completion_helpers, "build_api_kwargs", with_headers)

    result = prepare_rust_turn("runtime", session, frame)

    if eligible:
        plan, _ = result
        assert plan["params"]["session_affinity"] == {"session-id": "stored"}
        assert "extra_headers" not in plan["params"]
    else:
        assert result is None


@pytest.mark.parametrize("change", ["mode", "tools", "url", "headers", "backend", "version", "db"])
def test_ineligible_routes_leave_frame_unstaged(turn, monkeypatch, change):
    session, frame, _ = turn
    agent = session["agent"]
    if change == "mode":
        agent.api_mode = "codex_responses"
    elif change == "tools":
        agent.tools.append({"type": "function", "function": {"name": "terminal"}})
    elif change == "url":
        agent.base_url = "https://model.example/v1?api_key=synthetic"
    elif change == "headers":
        agent._client_kwargs["default_headers"] = {"X-Token": "synthetic"}
    elif change == "backend":
        monkeypatch.setattr("tools.file_tools._get_file_ops", lambda key: SimpleNamespace(env=SimpleNamespace(is_local=True)))
    elif change == "version":
        frame["history_version"] = 2
    elif change == "db":
        session["profile_home"] = str(Path(session["profile_home"]) / "another")
        from tui_gateway import server
        monkeypatch.setattr(server, "_session_home", lambda session: Path(session["profile_home"]))
    original = copy.deepcopy(frame)

    assert prepare_rust_turn("runtime", session, frame) is None
    assert frame == original
    assert "_rust_turn_stage" not in session


def _done(frame):
    return {
        "type": "turn.end", "status": "complete", "text": "File contents",
        "session_key": frame["session_key"], "request_id": frame["request_id"],
        "turn_id": frame["turn_id"],
        "messages": [*copy.deepcopy(frame["history"]),
            {"role": "user", "content": frame["text"]},
            {"role": "assistant", "content": "File contents"},
        ],
    }


def test_persistence_appends_only_new_messages_and_adopts_submit_row(turn):
    from agent.context_compressor import _DB_PERSISTED_MARKER

    session, frame, db = turn
    old = {"role": "assistant", "content": "Earlier answer"}
    db.append_messages_batch("stored", [old])
    session["history"] = [old]
    submitted = {"role": "user", "content": frame["text"]}
    db.append_messages_batch("stored", [submitted])
    submitted[_DB_PERSISTED_MARKER] = True
    session["_submit_user_row"] = submitted
    assert prepare_rust_turn("runtime", session, frame) is not None

    with rust_turn_lease(session, frame):
        persist_rust_turn(session, _done(frame))

    rows = db.get_messages_as_conversation("stored")
    assert [row["content"] for row in rows] == ["Earlier answer", "Read the file", "File contents"]
    assert [row["content"] for row in session["history"]] == [row["content"] for row in rows]
    assert session["history_version"] == 1
    assert "_submit_user_row" not in session
    assert session["agent"]._active_session_turn_lease_holder is None


def test_stale_completion_never_changes_transcript(turn):
    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    done = _done(frame)
    done["turn_id"] = "other-turn"

    with rust_turn_lease(session, frame):
        before = db.get_messages_as_conversation("stored")
        with pytest.raises(RustTurnConflict, match="stale"):
            persist_rust_turn(session, done)

    assert db.get_messages_as_conversation("stored") == before
    assert session["history"] == []


@pytest.mark.parametrize("corruption", ["prefix", "durable_marker", "system", "extra_user"])
def test_untrusted_child_transcript_cannot_skip_or_rewrite_rows(turn, corruption):
    session, frame, db = turn
    prior = {"role": "assistant", "content": "Prior turn"}
    session["history"] = [prior]
    assert prepare_rust_turn("runtime", session, frame) is not None
    done = _done(frame)
    if corruption == "prefix":
        done["messages"][0]["content"] = "Rewritten"
    elif corruption == "durable_marker":
        from agent.context_compressor import _DB_PERSISTED_MARKER
        done["messages"][-1][_DB_PERSISTED_MARKER] = True
    elif corruption == "system":
        done["messages"].append({"role": "system", "content": "Untrusted"})
    else:
        done["messages"].append({"role": "user", "content": "Injected"})

    with rust_turn_lease(session, frame):
        before = db.get_messages_as_conversation("stored")
        with pytest.raises(RustTurnConflict):
            persist_rust_turn(session, done)

    assert db.get_messages_as_conversation("stored") == before
    assert session["history"] == [prior]


def test_full_messages_is_authoritative_over_child_suffix(turn):
    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    done = _done(frame)
    done["new_messages"] = [{"role": "assistant", "content": "Forged suffix"}]

    with rust_turn_lease(session, frame):
        persist_rust_turn(session, done)

    assert [row["content"] for row in db.get_messages_as_conversation("stored")] == [
        "Read the file", "File contents",
    ]


def test_lost_durable_lease_fences_the_batch(turn):
    from hermes_state_errors import SessionTurnLeaseLostError

    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None

    with rust_turn_lease(session, frame):
        before = db.get_messages_as_conversation("stored")
        holder = session["agent"]._active_session_turn_lease_holder
        db.release_session_turn_lease("stored", holder)
        with pytest.raises(SessionTurnLeaseLostError):
            persist_rust_turn(session, _done(frame))

    assert db.get_messages_as_conversation("stored") == before
    assert session["history_version"] == 0


def test_lazy_build_uses_existing_agent_readiness_seam(turn, monkeypatch):
    from tui_gateway import server

    session, frame, _ = turn
    agent = session.pop("agent")
    monkeypatch.setattr(server, "_start_agent_build", lambda sid, record: record.update(agent=agent))
    monkeypatch.setattr(server, "_wait_agent_for_prompt", lambda *args: None)

    plan, env = prepare_rust_turn("runtime", session, frame)

    assert plan["model"] == agent.model
    assert env == {"OPENAI_API_KEY": "scoped-test-key"}


def test_model_sync_history_and_prompt_are_prepared_before_api_assembly(turn, monkeypatch):
    from tui_gateway import server

    session, frame, db = turn

    def sync(sid, record):
        record["agent"]._cached_system_prompt = "Updated runtime prompt"
        server._append_model_switch_marker(record, model="selected-model", provider="custom:fixture")

    monkeypatch.setattr(server, "_sync_agent_model_with_config", sync)

    plan, _ = prepare_rust_turn("runtime", session, frame)

    assert plan["system"].startswith("Updated runtime prompt")
    marker = session["history"][0]
    assert marker["display_kind"] == "model_switch"
    assert marker["content"] in frame["text"]
    assert frame["history_version"] == session["history_version"]
    with rust_turn_lease(session, frame):
        persist_rust_turn(session, _done(frame))
    assert [row.get("display_kind") for row in db.get_messages_as_conversation("stored")] == [
        "model_switch", None, None,
    ]


def test_store_failure_preserves_history_and_staging(turn, monkeypatch):
    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None

    def fail(*args, **kwargs):
        raise OSError("synthetic disk failure")

    with rust_turn_lease(session, frame):
        monkeypatch.setattr(db, "append_messages_batch", fail)
        with pytest.raises(OSError, match="synthetic disk"):
            persist_rust_turn(session, _done(frame))

    assert session["history"] == []
    assert session["history_version"] == 0
    assert "_rust_turn_stage" in session


def test_lease_can_exit_on_completion_thread_without_scope_tokens(turn):
    from agent.secret_scope import current_secret_scope

    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    context = rust_turn_lease(session, frame)
    before = current_secret_scope()
    context.__enter__()
    failures = []

    def complete():
        try:
            persist_rust_turn(session, _done(frame))
            context.__exit__(None, None, None)
        except BaseException as exc:
            failures.append(exc)

    thread = threading.Thread(target=complete)
    thread.start()
    thread.join(timeout=5)

    assert not thread.is_alive()
    assert failures == []
    assert current_secret_scope() == before
    assert len(db.get_messages_as_conversation("stored")) == 2


def test_admission_commits_accepted_user_before_host_dispatch(turn):
    from agent.persisted_turn_receipt import committed_row_id

    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None

    with rust_turn_lease(session, frame):
        stored = db.get_messages_as_conversation("stored", include_row_ids=True)
        accepted = session["_submit_user_row"]
        assert [row["content"] for row in stored] == ["Read the file"]
        assert committed_row_id(accepted) == stored[0]["_row_id"]
        assert session["agent"]._session_messages[-1] is accepted
        assert session["history"] == []


def test_admission_write_failure_never_dispatches_or_leaks_lease(turn, monkeypatch):
    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None

    def fail(*args, **kwargs):
        raise OSError("synthetic admission failure")

    monkeypatch.setattr(db, "append_messages_batch", fail)

    with pytest.raises(OSError, match="admission failure"):
        with rust_turn_lease(session, frame):
            pytest.fail("a failed admission reached dispatch")

    assert session["agent"]._active_session_turn_lease_holder is None
    assert db.get_messages_as_conversation("stored") == []


def test_provider_error_closes_the_saved_turn_without_claiming_success(turn):
    from agent.turn_failure_copy import FAILED_TURN_DISPLAY_KIND, failed_turn_notice

    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    failed = _done(frame)
    failed.update(status="error", error="HTTP 500: synthetic provider failure",
                  failure_reason="server_error", failure_retryable=True, text="Provider unavailable")
    failed["messages"] = failed["messages"][:-1]

    with rust_turn_lease(session, frame):
        receipt = persist_rust_turn(session, failed)

    stored = db.get_messages_as_conversation("stored", include_row_ids=True)
    assert [row["role"] for row in stored] == ["user", "assistant"]
    assert stored[-1]["display_kind"] == FAILED_TURN_DISPLAY_KIND
    assert stored[-1]["content"] == failed_turn_notice(stored[:1])
    assert stored[-1]["display_metadata"]["error_surface"]["code"] == "server_error"
    assert receipt is None
    assert session["agent"]._session_messages is session["history"]


def _tool_result_frame(frame):
    terminal = _done(frame)
    terminal["messages"][-1:] = [
        {"role": "assistant", "content": "", "tool_calls": [{
            "id": "read-1", "type": "function",
            "function": {"name": "read_file", "arguments": '{"path":"fixture.txt"}'},
        }]},
        {"role": "tool", "tool_call_id": "read-1", "tool_name": "read_file",
         "content": '{"content":"1|synthetic file contents"}'},
    ]
    return terminal


@pytest.mark.parametrize("empty_close", [False, True])
def test_interrupted_tool_transcript_uses_the_shared_hidden_close(turn, empty_close):
    from agent.agent_runtime_helpers_placeholders import hidden_interrupt_placeholder_row

    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    interrupted = _tool_result_frame(frame)
    interrupted.update(type="turn.end", status="interrupted", interrupted=True)
    actual_tool_content = interrupted["messages"][-1]["content"]
    if empty_close:
        interrupted["messages"].append({"role": "assistant", "content": ""})

    with rust_turn_lease(session, frame):
        receipt = persist_rust_turn(session, interrupted)

    stored = db.get_messages_as_conversation("stored")
    assert [row["role"] for row in stored] == ["user", "assistant", "tool", "assistant"]
    assert stored[2]["content"] == actual_tool_content
    assert stored[-1]["content"] == hidden_interrupt_placeholder_row()["content"]
    assert stored[-1]["api_content"] == hidden_interrupt_placeholder_row()["api_content"]
    assert stored[-1]["display_metadata"]["interrupted"] is True
    assert receipt is None


def test_provider_error_after_tool_retains_actual_rows_and_partial_notice(turn):
    from agent.turn_failure_copy import PARTIAL_FAILED_TURN_NOTICE

    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    failed = _tool_result_frame(frame)
    failed.update(status="error", error="synthetic provider failure")

    with rust_turn_lease(session, frame):
        receipt = persist_rust_turn(session, failed)

    stored = db.get_messages_as_conversation("stored")
    assert stored[2]["content"] == failed["messages"][-1]["content"]
    assert stored[-1]["content"] == PARTIAL_FAILED_TURN_NOTICE
    assert len(stored) == 4
    assert receipt is None


def test_crash_keeps_real_accepted_user_open_for_cold_rehydration(turn):
    session, frame, db = turn
    agent = session["agent"]
    assert prepare_rust_turn("runtime", session, frame) is not None
    with rust_turn_lease(session, frame):
        receipt = persist_rust_turn(session, {
            "type": "turn.error", "request_id": frame["request_id"],
            "reason": "crash", "message": "synthetic host exited",
        })
    stored = db.get_messages_as_conversation("stored", include_row_ids=True)
    assert [row["role"] for row in stored] == ["user"]
    assert stored[0]["content"] == "Read the file"
    assert receipt is None
    assert agent._active_session_turn_lease_holder is None
    assert session["agent"] is None


def test_crash_preserves_parent_mirror_and_requires_fresh_agent_sync(turn):
    session, frame, db = turn
    db.append_messages_batch("stored", [
        {"role": "user", "content": "Earlier request"},
        {"role": "assistant", "content": "Earlier reply"},
    ])
    session["history"] = db.get_messages_as_conversation("stored", include_row_ids=True)
    before = copy.deepcopy(session["history"])
    session["config_model_seen"] = {"model": "test-model"}
    session["_rust_parent_history"] = True
    assert prepare_rust_turn("runtime", session, frame) is not None
    with rust_turn_lease(session, frame):
        persist_rust_turn(session, {
            "type": "turn.error", "request_id": frame["request_id"], "reason": "crash",
        })

    stored = db.get_messages_as_conversation("stored", include_row_ids=True)
    assert [row["role"] for row in stored] == ["user", "assistant", "user"]
    assert stored[-1]["content"] == "Read the file"
    assert session["history"] == before
    assert session["history_version"] == 0
    assert session["agent"] is None
    assert "config_model_seen" not in session
    assert "_rust_parent_history" not in session


def test_unknown_tool_is_a_failed_boundary_without_retry_or_fabricated_call(turn):
    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None

    with rust_turn_lease(session, frame):
        receipt = persist_rust_turn(session, {
            "type": "turn.error", "request_id": frame["request_id"],
            "reason": "unsupported_tool", "message": "unsupported tool",
        })

    stored = db.get_messages_as_conversation("stored")
    assert [row["role"] for row in stored] == ["user", "assistant"]
    assert stored[-1]["display_kind"] == "failed_turn"
    assert all(not row.get("tool_calls") for row in stored)
    assert receipt is None


def test_missing_interrupted_tool_result_cannot_claim_completion(turn):
    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    interrupted = _tool_result_frame(frame)
    interrupted.update(status="interrupted", interrupted=True)
    interrupted["messages"].pop()

    with rust_turn_lease(session, frame):
        before = db.get_messages_as_conversation("stored")
        with pytest.raises(RustTurnConflict, match="missing actual tool results"):
            persist_rust_turn(session, interrupted)

    assert db.get_messages_as_conversation("stored") == before


@pytest.mark.parametrize("status", ["complete", "error", "interrupted"])
def test_tool_occurrence_identity_matches_normal_store_allocation(turn, monkeypatch, status):
    from uuid import UUID

    from agent import message_metadata
    from agent.message_sanitization import close_interrupted_tool_sequence
    from agent.turn_failure_copy import FAILED_TURN_DISPLAY_KIND, failed_turn_notice

    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    terminal = _tool_result_frame(frame)
    terminal.update(status=status, interrupted=status == "interrupted")
    if status == "complete":
        terminal["messages"].append({"role": "assistant", "content": "File contents"})
    reference = [
        {"role": "user", "content": "Read the file"},
        *copy.deepcopy(terminal["messages"][len(frame["history"]) + 1:]),
    ]
    db.create_session("reference", source="tui", model="test-model")

    def allocation_source():
        values = iter(UUID(int=value) for value in range(1, 20))
        return lambda: next(values)

    monkeypatch.setattr(message_metadata, "uuid4", allocation_source())
    db.append_messages_batch("reference", reference[:1])
    db.append_messages_batch("reference", reference[1:])
    stored_count = len(reference)
    if status == "interrupted":
        close_interrupted_tool_sequence(reference, stopped=True)
    elif status == "error":
        message_metadata.append_message(reference, {
            "role": "assistant", "content": failed_turn_notice(reference),
            "display_kind": FAILED_TURN_DISPLAY_KIND,
        })
    db.append_messages_batch("reference", reference[stored_count:])

    monkeypatch.setattr(message_metadata, "uuid4", allocation_source())
    with rust_turn_lease(session, frame):
        persist_rust_turn(session, terminal)

    stored = db.get_messages_as_conversation("stored")

    def identities(rows):
        return [
            (row["message_uid"], row.get("_tool_call_uids"), row.get("_tool_call_uid"))
            for row in rows
        ]

    assert identities(stored) == identities(reference)


@pytest.mark.parametrize("cursor", [None, 0])
def test_receipt_uses_the_existing_python_cursor_instead_of_forcing_one(turn, cursor):
    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    session["agent"]._persist_user_message_idx = cursor

    with rust_turn_lease(session, frame):
        receipt = persist_rust_turn(session, _done(frame))

    stored = db.get_messages_as_conversation("stored", include_row_ids=True)
    assert session["agent"]._last_flushed_db_idx == len(stored)
    if cursor is None:
        assert receipt is None
    else:
        assert receipt["complete"] is True
        assert receipt["user_row_id"] == stored[0]["_row_id"]
        assert receipt["final_assistant_row_id"] == stored[-1]["_row_id"]


def test_existing_cursor_from_an_older_turn_cannot_address_the_new_reply(turn):
    session, frame, db = turn
    db.append_messages_batch("stored", [
        {"role": "user", "content": "Earlier request"},
        {"role": "assistant", "content": "Earlier reply"},
    ])
    session["history"] = db.get_messages_as_conversation("stored", include_row_ids=True)
    session["agent"]._persist_user_message_idx = 0
    assert prepare_rust_turn("runtime", session, frame) is not None

    with rust_turn_lease(session, frame):
        receipt = persist_rust_turn(session, _done(frame))

    assert receipt is None
    assert session["agent"]._last_flushed_db_idx == 4
    assert len(db.get_messages_as_conversation("stored")) == 4


def test_interrupted_partial_text_is_saved_but_receipt_cannot_retire_the_turn(turn):
    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    session["agent"]._persist_user_message_idx = 0
    interrupted = _tool_result_frame(frame)
    interrupted.update(status="interrupted", interrupted=True, text="Actual partial answer")
    interrupted["messages"].append({"role": "assistant", "content": "Actual partial answer"})

    with rust_turn_lease(session, frame):
        receipt = persist_rust_turn(session, interrupted)

    stored = db.get_messages_as_conversation("stored", include_row_ids=True)
    assert stored[-1]["content"] == "Actual partial answer"
    assert stored[-1]["display_metadata"]["interrupted"] is True
    assert receipt["row_ids"][-1] == stored[-1]["_row_id"]
    assert receipt["complete"] is False
    assert "final_assistant_row_id" not in receipt
