"""Capability announcements from a completed parent agent build."""

from types import SimpleNamespace

import pytest


@pytest.mark.parametrize(
    "python_owner, rust_preparing",
    [(False, False), (True, False), (False, True)],
    ids=["before-handoff", "python-child-owns-turn", "rust-preparing"],
)
def test_parent_ready_announces_built_agent_capabilities(
    monkeypatch: pytest.MonkeyPatch, python_owner: bool, rust_preparing: bool,
) -> None:
    from tools.file_tools import READ_FILE_SCHEMA
    from tui_gateway import server

    agent = SimpleNamespace(tools=[{"type": "function", "function": READ_FILE_SCHEMA}])
    current = {
        "agent": agent,
        "_compute_host_active": python_owner,
        "_rust_host_preparing": rust_preparing,
        "_metadata_mirror": {"tools": {"child": ["child_tool"]}},
    }
    events = []
    monkeypatch.setattr(server, "_start_session_services", lambda *_: None)
    monkeypatch.setattr(server, "_schedule_mcp_late_refresh", lambda *_: None)
    monkeypatch.setattr(server, "_probe_config_health", lambda _: "")
    monkeypatch.setattr("hermes_cli.banner.get_update_result", lambda **_: None)
    monkeypatch.setattr("hermes_cli.banner.get_available_skills", lambda: {"test": ["example"]})
    monkeypatch.setattr("agent.credits_tracker.seed_credits_at_session_start", lambda _: None)
    monkeypatch.setattr(server, "_emit", lambda *args: events.append(args))

    server._announce_built_agent("session", "stored", current, agent)

    [(kind, sid, payload)] = events
    assert (kind, sid) == ("session.info", "session")
    assert payload["tools"] == {"file": ["read_file"]}
    assert payload["skills"] == {"test": ["example"]}
    if python_owner:
        assert server._session_info(agent, current)["tools"] == {"child": ["child_tool"]}
    if rust_preparing:
        assert current["_rust_ready_info"]["tools"] == payload["tools"]
