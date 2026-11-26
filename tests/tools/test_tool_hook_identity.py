"""Hook identity at tool entry points that do not receive a model tool-call ID."""

import json
import sys
from concurrent.futures import ThreadPoolExecutor
from threading import Event
from unittest.mock import Mock

import pytest

from agent.transports.hermes_tools_mcp_server import _build_server
from hermes_cli import plugins
from tools.code_execution_rpc import _default_dispatch
from tools.code_kernel import CellAuthority


@pytest.fixture
def manager(monkeypatch):
    manager = plugins.PluginManager()
    monkeypatch.setattr(plugins, "get_plugin_manager", lambda: manager)
    return manager


@pytest.mark.parametrize("caller", ["rpc", "kernel", "mcp"])
def test_hook_receives_distinct_ids_for_each_standalone_call(caller, manager, monkeypatch):
    seen = []

    def observe(tool_call_id, turn_id):
        seen.append((tool_call_id, turn_id))
        return {"action": "block", "message": "observed"}

    manager._hooks["pre_tool_call"] = [observe]
    if caller == "mcp":
        # Capture the handler the server registers without starting a stdio client.
        sdk = Mock()
        monkeypatch.setitem(sys.modules, "mcp.server", sdk)
        monkeypatch.setattr("model_tools.get_tool_definitions", lambda **kw: [
            {"type": "function", "function": {"name": "skill_view"}},
        ])
        server = _build_server()
        handler = server.add_tool.call_args.args[0]
        for _ in range(2):
            assert json.loads(handler())["error"] == "observed"
    else:
        dispatch = _default_dispatch("task") if caller == "rpc" else CellAuthority("task").dispatch
        for _ in range(2):
            assert json.loads(dispatch("skill_view", {}))["error"] == "observed"

    first, second = seen
    for a, b in zip(first, second):
        assert a and b and a != b, seen


def test_gate_blocks_duplicate_but_admits_independent_rpc(manager):
    entered, release = Event(), Event()
    calls = []

    def guard(args):
        label = args["name"]
        calls.append(label)
        if label == "first":
            entered.set()
            assert release.wait(30)
        return {"action": "block", "message": label}

    manager._hooks["pre_tool_call"] = [guard]
    with ThreadPoolExecutor(max_workers=1) as pool:
        for ids in ({}, {"tool_call_id": "held-call", "turn_id": "held-turn"}):
            entered.clear()
            release.clear()
            calls.clear()
            first = pool.submit(manager.invoke_hook, "pre_tool_call", tool_name="skill_view",
                                args={"name": "first"}, session_id="first-session", **ids)
            try:
                assert entered.wait(10)
                duplicate, = manager.invoke_hook("pre_tool_call", tool_name="skill_view",
                                                 args={"name": "duplicate"}, session_id="second-session", **ids)
                assert duplicate["action"] == "block"
                assert calls == ["first"]
                result = _default_dispatch("independent-task")("skill_view", {"name": "second"})
                assert json.loads(result)["error"] == "second"
                assert calls == ["first", "second"]
            finally:
                release.set()
                assert first.result(timeout=10) == [{"action": "block", "message": "first"}]
