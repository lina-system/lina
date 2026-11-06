"""Task-boundary permissions shared by ACP requests and Grok native hooks."""

import json

import pytest

from hermes_cli.worker.runners.acp import AcpRunner, _Session, _permission
from hermes_cli.worker.runners.base import Handle, Task


@pytest.mark.parametrize("mode", ["read-only", "workspace-write"])
@pytest.mark.parametrize("kind", ["read", "search", "fetch", "think"])
def test_read_operations_are_allowed_in_restricted_modes(tmp_path, mode, kind):
    assert _permission(tmp_path, mode, {"kind": kind})[0] is True


@pytest.mark.parametrize("raw", [{"command": "ls"}, {"command": "ls", "cwd": ""},
                                  {"command": "ls", "cwd": None}, {"command": "ls", "cwd": "  "}])
def test_execution_needs_a_known_nonempty_cwd(tmp_path, raw):
    assert _permission(tmp_path, "workspace-write", {"kind": "execute", "rawInput": raw})[0] is False


@pytest.mark.parametrize("mode,allowed", [("workspace-write", True), ("read-only", False), ("full", True)])
@pytest.mark.parametrize("tool", ["run_terminal_command", "bash"])
@pytest.mark.parametrize("tool_cwd,allowed_workspace", [("absent", True), (".", True), ("..", False), ("", False)])
def test_grok_execution_uses_tool_cwd_or_explicit_hook_cwd(tmp_path, mode, allowed, tool, tool_cwd, allowed_workspace):
    task = Task("hook-policy", tmp_path)
    task.work.mkdir()
    handle = Handle(task, 1, {"access": mode})
    runner = AcpRunner("grok", {"argv": ["grok", "agent", "stdio"], "flavor": "grok"})
    session = _Session(runner, handle)
    session.session_id = "live-session"
    replies = []
    session.send = replies.append
    raw = {"command": "git add hello.txt && git commit -m x"}
    if tool_cwd != "absent":
        raw["cwd"] = tool_cwd
    try:
        session.grok_hook("request", {"sessionId": "live-session", "hookCallbackId": "lina-access",
                          "hookEventName": "pre_tool_use", "toolName": tool, "toolInput": raw,
                          "cwd": str(task.work)})
        expected = allowed and (mode == "full" or allowed_workspace)
        assert replies[0]["result"]["decision"] == ("allow" if expected else "deny")
        event = json.loads(task.path("events.1.jsonl").read_text(encoding="utf-8-sig"))
        assert event["kind"] == ("permission_allowed" if expected else "permission_denied")
    finally:
        session.selector.close()


@pytest.mark.parametrize("tool", ["read_file", "list_dir", "grep", "web_search", "web_fetch", "think"])
def test_grok_read_tools_remain_available_in_read_only(tmp_path, tool):
    task = Task("read-hook", tmp_path)
    task.work.mkdir()
    session = _Session(AcpRunner("grok", {"argv": ["grok"]}), Handle(task, 1, {"access": "read-only"}))
    session.session_id = "live-session"
    replies = []
    session.send = replies.append
    try:
        session.grok_hook("request", {"sessionId": "live-session", "hookCallbackId": "lina-access",
                          "hookEventName": "pre_tool_use", "toolName": tool, "toolInput": {}})
        assert replies[0]["result"]["decision"] == "allow"
    finally:
        session.selector.close()


def test_execute_cwd_symlink_outside_worktree_is_denied(tmp_path):
    work = tmp_path / "work"
    work.mkdir()
    (work / "escape").symlink_to(tmp_path, target_is_directory=True)
    assert _permission(work, "workspace-write", {"kind": "execute", "rawInput": {
        "command": "python -m pytest -q", "cwd": "escape"}})[0] is False
