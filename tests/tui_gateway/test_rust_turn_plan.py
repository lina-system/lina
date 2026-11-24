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


@pytest.mark.parametrize(
    ("usage", "tool_text", "decision", "ineffective"),
    [
        ({"prompt_tokens": 9000, "initial_prompt_tokens": 400, "completion_tokens": 25},
         "small result", "allow", False),
        ({"prompt_tokens": 9000, "initial_prompt_tokens": 4900, "completion_tokens": 150},
         "small result", "pressure", False),
        ({"prompt_tokens": 9000, "initial_prompt_tokens": 4900, "completion_tokens": 150},
         "small result", "pressure", True),
        (None, "large synthetic result " * 2000, "pressure", False),
    ],
    ids=["first-hop-fits", "anchored-pressure", "rearmed-pressure", "usage-less-pressure"],
)
@pytest.mark.usefixtures("real_build_revision")
def test_native_continuation_uses_owned_python_pressure_policy(turn, monkeypatch, usage, tool_text, decision, ineffective):
    from contextlib import ExitStack

    from agent.usage_anchor import anchored_context_tokens
    from tui_gateway import server
    from tui_gateway.host_supervisor import HostSupervisor
    from tui_gateway.rust_host_supervisor import select_host

    session, frame, db = turn
    agent = session["agent"]
    cfg = {"compute_host_impl": "rust", "compute_host_heartbeat_secs": 15, "compute_host_respawn_max": 1}
    replies = []
    monkeypatch.setattr(HostSupervisor, "start", lambda _host: None)
    with ExitStack() as leases:
        host = select_host("runtime", session, cfg, frame, leases)
        assert host is not None
        session.update(_compute_host_turn_host=host, _compute_host_turn_id=frame["turn_id"])
        monkeypatch.setattr(host, "_send_frame", replies.append)
        host._handle_host_frame({"type": "turn.started", "sid": "runtime", "request_id": frame["turn_id"]})
        agent.context_compressor.threshold_tokens = 5000
        agent.compression_enabled = True
        agent._compression_feasibility_checked = True
        if ineffective:
            agent.context_compressor._record_ineffective_compression_verdict(2)
            assert agent.context_compressor.should_compress_info(5050) == (False, "ineffective")
        terminal = _tool_result_frame(frame)
        terminal["messages"][-1]["content"] = tool_text
        check = {
            "type": "context.preflight", "sid": "runtime", "turn_id": frame["turn_id"],
            "request_id": "preflight", "priced_prefix_count": len(frame["history"]) + 1,
            "usage": usage, "messages": terminal["messages"], "summary": False,
        }
        host._handle_host_frame({**check, "turn_id": "previous-turn", "request_id": "stale"})
        if ineffective:
            assert agent.context_compressor.should_compress_info(5050) == (False, "ineffective")
        host._handle_host_frame(check)
        assert [(reply["request_id"], reply["decision"]) for reply in replies] == [
            ("stale", "unavailable"), ("preflight", decision),
        ]
        terminal["context_response"] = {
            "priced_prefix_count": check["priced_prefix_count"], "usage": usage,
        }
        if decision == "pressure":
            terminal.update(type="turn.end", status="error", context_failure="context_pressure",
                            error="Paused before the continuation.")
            assert persist_rust_turn(session, terminal) is None
        else:
            terminal["context_response"] = {"priced_prefix_count": len(check["messages"]), "usage": None}
            terminal["messages"].append({"role": "assistant", "content": terminal["text"]})
            persist_rust_turn(session, terminal)
            before = getattr(agent, "session_api_calls", 0)
            server._apply_rust_turn_usage(session, agent, {"calls": 2, "context_used": 400})
            assert agent.session_api_calls == before + 2
            assert not agent.context_compressor.should_defer_preflight_to_real_usage(5050)

    if decision == "pressure":
        stored = db.get_messages_as_conversation("stored")
        assert [row["role"] for row in stored] == ["user", "assistant", "tool", "assistant"]
        assert stored[2]["content"] == tool_text
        if usage is not None:
            assert anchored_context_tokens(stored, agent._usage_anchor) >= 5000
        with ExitStack() as leases:
            assert select_host("runtime", session, cfg, {}, leases) is None
        assert server._prepare_inline_after_compute_host(session)
        assert session["history"][2]["content"] == tool_text
        if usage is None:
            _assert_python_compacts_before_resumed_send(agent, session["history"], monkeypatch)
    else:
        host.shutdown()


def _assert_python_compacts_before_resumed_send(agent, history, monkeypatch):
    from agent.turn_preflight_gate import run_preflight_gate
    from agent.turn_request_assembly import assemble_api_request
    from tui_gateway import server

    class CompressionStarted(Exception):
        pass

    def record_compression(*_args, trigger, **_kwargs):
        raise CompressionStarted(trigger)

    text = "Continue with the file result"
    messages = [*history, {"role": "user", "content": text}]
    system = agent._cached_system_prompt
    assembled = assemble_api_request(
        agent, messages=messages, current_turn_user_idx=len(history), _ext_prefetch_cache="",
        _plugin_user_context="", moa_config=None, active_system_prompt=system, original_user_message=text,
        pending_moa_prepared_request=None, request_logger=server.logger)
    assert not agent._request_pressure_anchored
    assert assembled.request_pressure_tokens >= agent.context_compressor.threshold_tokens
    with monkeypatch.context() as scoped:
        scoped.setattr(agent, "_compress_context", record_compression)
        with pytest.raises(CompressionStarted, match="pre_api"):
            run_preflight_gate(
                agent, request_pressure_tokens=assembled.request_pressure_tokens, _moa_prepared_request=None,
                pending_moa_prepared_request=None, messages=messages, system_message=None, user_message=text,
                active_system_prompt=system, conversation_history=history, api_call_count=1,
                compression_attempts=0, max_compression_attempts=3, effective_task_id=None, final_response=None,
                failed=False, _turn_exit_reason=None, _compression_timeout_exhausted=False,
                _preflight_compression_blocked=False, _provider_overflow_recovery_pending=False,
                _last_preflight_pressure=None)


def test_invalid_native_terminal_context_response_cannot_append_rows(turn):
    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None
    terminal = _done(frame)
    terminal["context_response"] = {
        "usage": {"prompt_tokens": 123},
        "priced_prefix_count": len(terminal["messages"]) + 1,
    }
    with rust_turn_lease(session, frame):
        before = db.get_messages_as_conversation("stored")
        with pytest.raises(RustTurnConflict, match="invalid priced prefix"):
            persist_rust_turn(session, terminal)
        assert db.get_messages_as_conversation("stored") == before
    assert session["history"] == []


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


@pytest.mark.usefixtures("real_build_revision")
def test_crash_preserves_parent_mirror_and_requires_fresh_agent_sync(turn, monkeypatch):
    import time
    from contextlib import ExitStack

    from tui_gateway import server
    from tui_gateway.rust_host_supervisor import RustHostSupervisor, select_host

    session, frame, db = turn
    agent = session["agent"]
    ready = threading.Event()
    ready.set()
    session.update(agent_ready=ready, agent_build_started=True, agent_error="stale build error")
    cfg = {"compute_host_impl": "rust", "compute_host_heartbeat_secs": 15, "compute_host_respawn_max": 1}
    host = RustHostSupervisor(
        "runtime", session, home=session["profile_home"],
        credentials={"OPENAI_API_KEY": "scoped-test-key"}, endpoint=agent.base_url, cfg=cfg)
    monkeypatch.setattr(host, "start", lambda: None)
    restart_times = [time.monotonic()]
    host._restart_times = restart_times
    host._replacement_ready = True
    session["_rust_host"] = host
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

    built = []

    def make_agent(*args, **kwargs):
        replacement = type(agent)(
            model="test-model", provider="custom", api_mode="chat_completions",
            api_key="scoped-test-key", base_url=agent.base_url,
            session_id="stored", session_db=db, quiet_mode=True,
            skip_context_files=True, skip_memory=True, checkpoints_enabled=False,
            reasoning_config={"enabled": False},
        )
        replacement._cached_system_prompt = "Stored system prompt"
        replacement._use_prompt_caching = False
        built.append(replacement)
        return replacement

    monkeypatch.setitem(server._sessions, "runtime", session)
    monkeypatch.setattr(server, "_make_agent", make_agent)
    monkeypatch.setattr(server, "_bind_build_profile_scopes", lambda _: None)
    monkeypatch.setattr(server, "_open_profile_session_db", lambda _: None)
    monkeypatch.setattr(server, "_wire_session_agent", lambda *_: False)
    monkeypatch.setattr(server, "_announce_built_agent", lambda *_: None)
    monkeypatch.setattr("tui_gateway.entry.ensure_mcp_discovery_started", lambda: None)
    monkeypatch.setattr(
        server, "_get_compute_host_supervisor",
        lambda _: pytest.fail("cold builder failure selected the Python supervisor"))
    next_frame = {
        **frame, "request_id": "next-request", "turn_id": "next-turn",
        "text": "A new explicit request", "history": copy.deepcopy(session["history"]),
        "history_version": session["history_version"],
    }
    try:
        with ExitStack() as leases:
            selected = select_host("runtime", session, cfg, next_frame, leases)
            assert selected is host
            assert session["_rust_host"] is host
            assert host._restart_times is restart_times
            assert host._replacement_ready is True
            assert len(built) == 1
            assert session["agent"] is built[0]
            assert session["agent_ready"] is not ready
            assert session["agent_ready"].is_set()
            assert "agent_error" not in session
            assert db.get_session("stored")["ended_at"] is None
    finally:
        for replacement in built:
            replacement.close()


def test_host_error_without_transcript_persists_failure_without_fabricated_call(turn):
    session, frame, db = turn
    assert prepare_rust_turn("runtime", session, frame) is not None

    with rust_turn_lease(session, frame):
        receipt = persist_rust_turn(session, {
            "type": "turn.error", "request_id": frame["request_id"],
            "reason": "model_endpoint", "message": "invalid model endpoint",
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
