"""ACP runner contracts against a real JSON-RPC subprocess (lina#221)."""

import json
import os
import sys
from pathlib import Path
from threading import Condition

import pytest

from hermes_cli.worker.runners import build
from hermes_cli.worker.runners.base import Handle, RunnerError, Task

pytestmark = pytest.mark.platforms("posix")

AGENT = r'''
import json, os, sys
from pathlib import Path
session = "session-one"
pending = None
permission = None
scenario = sys.argv[1]
log = Path(sys.argv[2])
def send(value):
    print(json.dumps({"jsonrpc": "2.0", **value}), flush=True)
def result(i, value):
    send({"id": i, "result": value})
def finish(i, text="done"):
    send({"method": "session/update", "params": {"sessionId": session, "update": {
        "sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": text}}}})
    Path("report.md").write_text("# Report\n\n## Next\nFinished.\n")
    result(i, {"stopReason": "end_turn"})
for line in sys.stdin:
    message = json.loads(line)
    with log.open("a") as stream:
        stream.write(json.dumps(message) + "\n")
    method, params, i = message.get("method"), message.get("params", {}), message.get("id")
    if method == "initialize":
        caps = {"loadSession": scenario != "fallback", "sessionCapabilities": {"close": {}}}
        if scenario == "grok":
            caps["_meta"] = {"x.ai/hooks": {"blockingEvents": ["pre_tool_use"], "decisions": ["deny"]}}
        if scenario == "resume":
            caps = {"sessionCapabilities": {"resume": {}, "close": {}}}
        result(i, {"protocolVersion": 1, "agentCapabilities": caps})
    elif method in ("session/new", "session/load", "session/resume"):
        if scenario == "grok":
            assert params["_meta"]["x.ai/hooks"]["PreToolUse"][0]["hookCallbackIds"] == ["lina-access"]
        session = params.get("sessionId", "session-one")
        result(i, {"sessionId": session})
    elif method == "session/prompt":
        text = params["prompt"][0]["text"]
        if scenario == "crash":
            sys.exit(17)
        elif text.startswith("hang"):
            pending = i
            send({"method": "session/update", "params": {"sessionId": session, "update": {
                "sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "ready"}}}})
        elif text.startswith("ask"):
            Path("question.md").write_text("Which colour?")
            result(i, {"stopReason": "end_turn"})
        elif text.startswith("hook:"):
            permission = (i, {})
            send({"id": "hook", "method": "x.ai/hooks/run", "params": {
                "sessionId": session, "hookCallbackId": "lina-access", "hookEventName": "pre_tool_use",
                "toolName": "search_replace", "toolInput": {"file_path": "hello.txt"}}})
        elif text.startswith("permission:"):
            call = json.loads(text.split("\n", 1)[0].split(":", 1)[1])
            permission = (i, call)
            send({"id": "permission", "method": "session/request_permission", "params": {
                "sessionId": session, "toolCall": call, "options": [
                    {"optionId": "once", "name": "Once", "kind": "allow_once"},
                    {"optionId": "always", "name": "Always", "kind": "allow_always"},
                    {"optionId": "no", "name": "No", "kind": "reject_once"}]}})
        elif text.startswith("refusal"):
            result(i, {"stopReason": "refusal"})
        else:
            finish(i, text.split("\n", 1)[0])
    elif method == "session/cancel":
        if pending is not None:
            result(pending, {"stopReason": "cancelled"})
            pending = None
    elif method == "session/close":
        result(i, {})
        break
    elif i == "permission":
        if message["result"]["outcome"].get("optionId") == "once":
            Path("allowed.txt").write_text("yes")
        finish(permission[0])
    elif i == "hook":
        if message["result"].get("decision") == "allow":
            Path("hello.txt").write_text("hi")
        finish(permission[0])
'''


@pytest.fixture
def lane(tmp_path, monkeypatch):
    from hermes_cli.worker.runners.acp import _Session
    condition = Condition()
    published = _Session.publish
    received = _Session.message
    tasks = []

    def publish(session, state):
        with condition:
            published(session, state)
            condition.notify_all()

    def message(session, value):
        with condition:
            received(session, value)
            condition.notify_all()

    monkeypatch.setattr(_Session, "publish", publish)
    monkeypatch.setattr(_Session, "message", message)

    def create(scenario="normal", mode="workspace-write", prompt="hello"):
        directory = tmp_path / str(len(tasks))
        directory.mkdir()
        task = Task("acp-contract", directory)
        task.work.mkdir()
        log = task.path("wire.jsonl")
        spec = {"type": "acp", "argv": [sys.executable, "-u", "-c", AGENT, scenario, str(log)]}
        if scenario == "grok":
            spec["flavor"] = "grok"
        runner = build("fake", spec, {"stall_grace_s": 10})
        handle = runner.start(task, prompt, None, access=mode)
        tasks.append((runner, handle))
        return runner, handle, spec

    def wait(runner, handle, state):
        with condition:
            assert condition.wait_for(
                lambda: json.loads(handle.task.path(f"acp.{handle.run}.json").read_text(encoding="utf-8-sig"))["state"] == state,
                timeout=15,
            ), "ACP state publication timed out"
        assert runner.state(handle) == state

    def ready(runner, handle):
        with condition:
            assert condition.wait_for(lambda: runner._sessions[handle.run].final == "ready", timeout=15)

    yield create, wait, ready
    for runner, handle in tasks:
        for session in runner._sessions.values():
            runner.stop(session.handle)
            assert not session.thread.is_alive()
            assert session.proc.poll() is not None


def wire(handle):
    return [json.loads(line) for line in handle.task.path("wire.jsonl").read_text(encoding="utf-8-sig").splitlines()]


def test_lifecycle_returns_artifacts_and_reloads_session(lane):
    create, wait, _ = lane
    runner, handle, spec = create()
    wait(runner, handle, "done")
    assert runner.result(handle)["report.md"].is_file()
    other = build("fake", spec)
    action, second = other.follow_up(Handle.load(handle.task), "second", run=2)
    try:
        wait(other, second, "done")
        assert action == "started"
        assert any(row.get("method") == "session/load" for row in wire(second))
        assert other.result(second)["final.md"].read_text(encoding="utf-8-sig").strip() == "second"
    finally:
        other.stop(second)


@pytest.mark.parametrize("scenario,method", [("resume", "session/resume"), ("fallback", "session/new")])
def test_next_run_uses_advertised_resume_or_context_fallback(lane, scenario, method):
    create, wait, _ = lane
    runner, handle, _ = create(scenario, prompt="remember-this")
    wait(runner, handle, "done")
    _, second = runner.follow_up(handle, "next")
    wait(runner, second, "done")
    messages = wire(second)
    opens = [row for row in messages if row.get("method") in ("session/new", "session/resume", "session/load")]
    assert opens[-1]["method"] == method
    if scenario == "fallback":
        prompt = [row for row in messages if row.get("method") == "session/prompt"][-1]["params"]["prompt"][0]["text"]
        assert "remember-this" in prompt and "Finished." in prompt


def test_live_steer_cancels_then_prompts_the_same_session(lane):
    create, wait, ready = lane
    runner, handle, spec = create(prompt="hang")
    ready(runner, handle)
    other = build("fake", spec)
    assert other.follow_up(Handle.load(handle.task), "new instruction")[0] == "steered"
    wait(runner, handle, "done")
    messages = wire(handle)
    prompts = [row for row in messages if row.get("method") == "session/prompt"]
    assert len(prompts) == 2 and prompts[0]["params"]["sessionId"] == prompts[1]["params"]["sessionId"]
    assert runner.result(handle)["final.md"].read_text(encoding="utf-8-sig").strip() == "new instruction"


def test_question_answer_continues_same_live_session(lane):
    create, wait, _ = lane
    runner, handle, spec = create(prompt="ask")
    wait(runner, handle, "waiting_human")
    other = build("fake", spec)
    assert other.answer(Handle.load(handle.task), "blue")["action"] == "answer"
    wait(runner, handle, "done")
    assert not (handle.task.work / "question.md").exists()
    assert runner.result(handle)["final.md"].read_text(encoding="utf-8-sig").strip() == "blue"
    assert sum(row.get("method") == "session/new" for row in wire(handle)) == 1


@pytest.mark.parametrize("mode,target,kind,allowed", [
    ("read-only", "hello.txt", "edit", False),
    ("workspace-write", "hello.txt", "edit", True),
    ("workspace-write", "../outside.txt", "edit", False),
    ("workspace-write", "hello.txt", "other", False),
    ("full", "../outside.txt", "other", True),
])
def test_permission_responses_never_exceed_access(lane, mode, target, kind, allowed):
    create, wait, _ = lane
    call = {"toolCallId": "edit-1", "kind": kind, "locations": [{"path": target}]}
    runner, handle, _ = create(mode=mode, prompt="permission:" + json.dumps(call))
    wait(runner, handle, "done")
    assert (handle.task.work / "allowed.txt").exists() == allowed
    reply = next(row for row in wire(handle) if row.get("id") == "permission")
    assert reply["result"]["outcome"] == {"outcome": "selected", "optionId": "once" if allowed else "no"}
    events = handle.task.path("events.1.jsonl").read_text(encoding="utf-8-sig")
    assert ("permission_allowed" if allowed else "permission_denied") in events


def test_unknown_permission_target_is_rejected(lane):
    create, wait, _ = lane
    runner, handle, _ = create(prompt='permission:{"toolCallId":"x","kind":"edit"}')
    wait(runner, handle, "done")
    assert not (handle.task.work / "allowed.txt").exists()


def test_agent_death_is_failed_not_done(lane):
    create, wait, _ = lane
    runner, handle, _ = create("crash")
    wait(runner, handle, "failed")
    assert "closed stdout" in handle.task.path("stderr.1.txt").read_text(encoding="utf-8-sig")


def test_stop_sends_cancel_and_reaps_process(lane):
    create, wait, ready = lane
    runner, handle, _ = create(prompt="hang")
    ready(runner, handle)
    runner.stop(handle)
    wait(runner, handle, "cancelled")
    assert any(row.get("method") == "session/cancel" for row in wire(handle))
    with pytest.raises(ProcessLookupError):
        os.kill(handle.data["pid"], 0)


def test_refusal_is_failed(lane):
    create, wait, _ = lane
    runner, handle, _ = create(prompt="refusal")
    wait(runner, handle, "failed")


def test_access_cannot_escalate_on_follow_up(lane):
    create, wait, _ = lane
    runner, handle, _ = create(mode="read-only")
    wait(runner, handle, "done")
    with pytest.raises(RunnerError, match="cannot change access"):
        runner.follow_up(handle, "write", access="full")


@pytest.mark.parametrize("mode,allowed", [("workspace-write", True), ("read-only", False)])
def test_grok_native_tools_obey_hook_even_without_permission_request(lane, mode, allowed):
    create, wait, _ = lane
    runner, handle, _ = create("grok", mode=mode, prompt="hook:create")
    wait(runner, handle, "done")
    assert (handle.task.work / "hello.txt").exists() == allowed
    assert not any(row.get("id") == "permission" for row in wire(handle))


def test_symlink_escape_is_denied(lane):
    create, wait, ready = lane
    runner, handle, _ = create(prompt="hang")
    ready(runner, handle)
    (handle.task.work / "escape").symlink_to(handle.task.dir, target_is_directory=True)
    runner.follow_up(handle, 'permission:{"toolCallId":"x","kind":"edit","locations":[{"path":"escape/secret"}]}')
    wait(runner, handle, "done")
    assert not (handle.task.work / "allowed.txt").exists()


@pytest.mark.parametrize("command", ["python -m pytest -q", "git commit -m x", "npm run build"])
@pytest.mark.parametrize("mode,cwd,allowed", [
    ("workspace-write", ".", True), ("workspace-write", "..", False),
    ("workspace-write", "", False), ("workspace-write", None, False),
    ("read-only", ".", False),
])
def test_execution_uses_access_and_working_directory(lane, command, mode, cwd, allowed):
    create, wait, _ = lane
    call = {"toolCallId": "exec", "kind": "execute", "rawInput": {"command": command}}
    if cwd is not None:
        call["rawInput"]["cwd"] = cwd
    runner, handle, _ = create(mode=mode, prompt="permission:" + json.dumps(call))
    wait(runner, handle, "done")
    assert (handle.task.work / "allowed.txt").exists() == allowed
