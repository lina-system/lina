"""Permission RPC dialogs are machine decisions, never human answers (lina#220)."""

import json
from pathlib import Path
import shutil

import pytest

from hermes_cli.worker.runners.base import Handle, RunnerError, Task
from hermes_cli.worker.runners.omo import OmoRunner


RPC = """
import { readFileSync, writeFileSync } from 'node:fs';
const file = process.env.PERMISSION_FAKE;
const load = () => JSON.parse(readFileSync(file, 'utf8'));
export class RpcClient {
  async start() {}
  async stop() {}
  async openSession() {}
  async getState() { return load().state; }
  async getMessages() { return load().messages || []; }
  async sendExtensionUIResponse(frame) {
    const data = load();
    data.responses.push(frame.value);
    if (frame.value === 'Allow once') writeFileSync(data.target, 'ok');
    data.state = {pendingDialogs: [], pendingQuestions: [], isStreaming: false,
                  lifecycle: {state: 'done', seq: 2, waitingOn: {}}};
    writeFileSync(file, JSON.stringify(data));
  }
}
"""
REF = """
export async function resolveSessionRef(client, ref) {
  return {kind: 'live', row: {sessionPath: '/fake/session.jsonl', cwd: process.env.PERMISSION_CWD}};
}
"""


@pytest.fixture
def lane(tmp_path, monkeypatch):
    node = shutil.which("node")
    assert node, "the RPC helper needs Node"
    task = Task("permission-test", tmp_path / "task")
    task.work.mkdir(parents=True)
    rpc = tmp_path / "engine/dist/modes/rpc"
    rpc.mkdir(parents=True)
    (tmp_path / "engine/package.json").write_text('{"type":"module"}')
    (rpc / "rpc-client.js").write_text(RPC)
    (rpc / "host-session-ref.js").write_text(REF)
    record = tmp_path / "permission.json"
    runner = OmoRunner("omo", {"node": node, "engine": str(tmp_path / "engine"), "bin": "unused"}, root=tmp_path)
    runner.env.update(PERMISSION_FAKE=str(record), PERMISSION_CWD=str(task.work))
    monkeypatch.setattr("hermes_cli.worker.runners.omo_settle.live_work_processes", lambda task: [])

    def session(args, timeout):
        data = json.loads(record.read_text())
        if args[0] == "state":
            return {"action": "state", "state": data["state"]}
        return {"action": "read", "entries": []}

    monkeypatch.setattr(runner, "session", session)
    return runner, task, record


def pending(lane, mode, target, tool="edit"):
    runner, task, record = lane
    dialog = {"id": "permission-1", "method": "select", "toolCallId": "tool-1",
              "title": f"Permission required: {tool}\n\nFile: {target}\n\nPatterns:\n  - {target}",
              "options": ["Allow once", "Allow always", "Deny", "Deny with feedback"]}
    record.write_text(json.dumps({"state": {"pendingDialogs": [dialog], "pendingQuestions": [], "isStreaming": True,
        "lifecycle": {"state": "waiting_human", "seq": 1, "waitingOn": {"human": [{"requestId": dialog["id"]}]}}},
        "responses": [], "target": str((task.work / target).resolve())}))
    handle = Handle(task, 1, {"ref": "owned", "access": mode})
    handle.save()
    return runner, task, record, handle


@pytest.mark.parametrize(("mode", "target", "reply"), [
    ("read-only", "probe.txt", "Deny"),
    ("workspace-write", "probe.txt", "Allow once"),
    ("workspace-write", "../outside.txt", "Deny"),
])
def test_permission_is_settled_without_human_state(lane, mode, target, reply):
    runner, task, record, handle = pending(lane, mode, target)
    assert runner.state(handle) == "running"
    assert runner.state(handle) == "done"
    data = json.loads(record.read_text())
    assert data["responses"] == [reply]
    assert Path(data["target"]).exists() == (reply == "Allow once")
    events = [json.loads(line) for line in task.path("events.1.jsonl").read_text().splitlines()]
    decision = next(event for event in events if event["kind"].startswith("permission_"))
    assert (decision["kind"], decision["tool"], decision["target"], decision["mode"]) == (
        "permission_allowed" if reply == "Allow once" else "permission_denied", "edit", target, mode)


def test_answer_cannot_approve_a_permission_dialog(lane):
    runner, _, record, handle = pending(lane, "read-only", "probe.txt")
    with pytest.raises(RunnerError, match="permission_dialog"):
        runner.answer(handle, "Allow once")
    assert json.loads(record.read_text())["responses"] == []


def test_workspace_symlink_escape_is_denied(lane):
    _, task, _ = lane
    (task.work / "escape").symlink_to(task.dir, target_is_directory=True)
    runner, _, record, handle = pending(lane, "workspace-write", "escape/outside.txt")
    assert runner.state(handle) == "running"
    assert json.loads(record.read_text())["responses"] == ["Deny"]


def test_unknown_permission_is_denied_even_in_full(lane):
    runner, _, record, handle = pending(lane, "full", "probe.txt", tool="unknown")
    assert runner.state(handle) == "running"
    assert json.loads(record.read_text())["responses"] == ["Deny"]


@pytest.mark.parametrize(("code", "expected"), [
    ('await tool.bash({"command":"ls && git status --short && git add local.txt && git commit -m test"})', "Allow once"),
    ('await tool.bash({"command":"touch ../outside"})', "Deny"),
    ('await tool.bash({"command":"git add ../outside"})', "Deny"),
    ('await tool.bash({"command":"git add .."})', "Deny"),
    ('await tool.bash({"command":"git commit -m test --git-dir other"})', "Deny"),
    ('await tool.bash({"command":"ls"}); await Bun.write("../outside", "bad")', "Deny"),
    ('await tool.bash({command:"ls"})', "Deny"),
])
def test_eval_bridge_allows_only_literal_confined_shell_operations(lane, code, expected):
    runner, _, record, handle = pending(lane, "workspace-write", "probe.txt", tool="eval")
    data = json.loads(record.read_text())
    data["messages"] = [{"role": "assistant", "content": [{"type": "toolCall", "id": "tool-1", "name": "eval",
        "arguments": {"language": "js", "code": code}}]}]
    record.write_text(json.dumps(data))
    assert runner.state(handle) == "running"
    assert json.loads(record.read_text())["responses"] == [expected]


@pytest.mark.parametrize("waiting", [
    {"human": [{"requestId": "question:permission-1"}]},
    {"background": [{"source": "delivery:omo-kibitzer:recall", "activeCount": 1, "items": []}]},
])
def test_resolved_permission_hold_finishes_only_after_final_response(lane, monkeypatch, waiting):
    runner, task, record, handle = pending(lane, "read-only", "probe.txt")
    assert runner.state(handle) == "running"
    data = json.loads(record.read_text())
    data["state"]["lifecycle"] = {"state": "waiting_human" if "human" in waiting else "waiting_background",
        "seq": 3, "waitingOn": waiting}
    record.write_text(json.dumps(data))
    original = runner.session

    def session(args, timeout):
        if args[0] == "read":
            return {"action": "read", "entries": [{"message": {"role": "assistant", "stopReason": "stop",
                "content": [{"type": "text", "text": "Write denied; finished."}]}}]}
        return original(args, timeout)

    monkeypatch.setattr(runner, "session", session)
    data["state"]["pendingDialogs"] = [{"id": "ordinary-question", "method": "input", "title": "Which file?"}]
    record.write_text(json.dumps(data))
    assert runner.state(handle) == "waiting_human"
    data["state"]["pendingDialogs"] = []
    data["state"]["lifecycle"]["waitingOn"]["children"] = 1
    record.write_text(json.dumps(data))
    assert runner.state(handle) == "running"
    data["state"]["lifecycle"]["waitingOn"]["children"] = 0
    record.write_text(json.dumps(data))
    assert runner.state(handle) == "done"
    assert not (task.work / "probe.txt").exists()
