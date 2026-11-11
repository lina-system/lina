"""Version-bound ACP admission before task delivery (lina#451)."""

import json
import sys

import pytest

from hermes_cli import lina_runners
from hermes_cli.worker.runners.acp import AcpRunner
from hermes_cli.worker.runners.base import RunnerError, Task

pytestmark = pytest.mark.platforms("posix")

AGENT = '''
import json, sys
from pathlib import Path
if "--version" in sys.argv:
    print(VERSION)
    raise SystemExit
Path(__file__).with_suffix(".started").write_text("started")
pending = None
for line in sys.stdin:
    message = json.loads(line)
    method = message.get("method")
    result = {}
    if method == "initialize":
        result = {"protocolVersion": 1, "agentCapabilities": {}}
    elif method == "session/new":
        result = {"sessionId": "test-session"}
    elif method == "session/prompt":
        if PERMISSIONS:
            pending = message["id"]
            print(json.dumps({"jsonrpc": "2.0", "id": "permission",
                "method": "session/request_permission", "params": {
                "sessionId": "test-session", "toolCall": {
                    "kind": "edit", "locations": [{"path": "inside.txt"}]},
                "options": [{"kind": "allow_once", "optionId": "yes"},
                            {"kind": "reject_once", "optionId": "no"}]}}), flush=True)
            continue
        Path("../outside.txt").write_text("unmediated")
        result = {"stopReason": "end_turn"}
    elif message.get("id") == "permission":
        if message["result"]["outcome"].get("optionId") == "yes":
            Path("inside.txt").write_text("allowed")
        message["id"] = pending
        result = {"stopReason": "end_turn"}
    elif method == "session/cancel":
        continue
    print(json.dumps({"jsonrpc": "2.0", "id": message["id"], "result": result}), flush=True)
'''


def make_runner(tmp_path, version="0.2.1-rc.5", permissions=False, flavor="dsh"):
    binary = tmp_path / "agent"
    binary.write_text(f"#!{sys.executable}\nVERSION = {version!r}\nPERMISSIONS = {permissions!r}\n" + AGENT)
    binary.chmod(0o755)
    task = Task("admission", tmp_path / "task")
    task.work.mkdir(parents=True)
    runner = AcpRunner("dsh-test", {"argv": [str(binary), "acp"], "flavor": flavor}, root=tmp_path)
    return runner, task, binary


@pytest.mark.parametrize("mode", ["read-only", "workspace-write"])
def test_unmediated_version_refuses_before_start(tmp_path, mode):
    runner, task, binary = make_runner(tmp_path)
    handle = None
    try:
        with pytest.raises(RunnerError, match=f"dsh.*{mode}"):
            handle = runner.start(task, "write outside", None, access=mode)
        assert not binary.with_suffix(".started").exists()
        assert not task.path("outside.txt").exists()
        assert not task.path("handle.json").exists()
    finally:
        if handle is not None:
            runner.stop(handle)


def test_unmediated_version_full_still_runs(tmp_path):
    runner, task, _ = make_runner(tmp_path)
    handle = runner.start(task, "write outside", None, access="full")
    try:
        runner.result(handle)
        assert runner.state(handle) == "done"
        assert task.path("outside.txt").read_text() == "unmediated"
    finally:
        runner.stop(handle)


@pytest.mark.parametrize("mode,allowed", [("read-only", False), ("workspace-write", True)])
def test_reobserved_future_version_starts_and_enforces_permissions(tmp_path, monkeypatch, mode, allowed):
    monkeypatch.setattr(lina_runners, "ACP_PERMISSION_OBSERVATIONS",
                        {"dsh": {"99.0.0": True}}, raising=False)
    runner, task, _ = make_runner(tmp_path, version="99.0.0", permissions=True)
    handle = runner.start(task, "edit inside", None, access=mode)
    try:
        runner.result(handle)
        assert runner.state(handle) == "done"
        assert (task.work / "inside.txt").exists() is allowed
        events = [json.loads(line) for line in task.path("events.1.jsonl").read_text().splitlines()]
        assert any(event["kind"] == ("permission_allowed" if allowed else "permission_denied") for event in events)
    finally:
        runner.stop(handle)


def test_unknown_version_requires_observation_before_narrowing(tmp_path):
    runner, _, binary = make_runner(tmp_path, version="99.0.1")
    supported, reason = runner.honours("read-only")
    assert supported is False
    assert "99.0.1" in reason and "read-only" in reason
    assert not binary.with_suffix(".started").exists()


def test_managed_executable_is_probed_instead_of_path_copy(tmp_path, monkeypatch):
    runner, _, binary = make_runner(tmp_path)
    shim = tmp_path / "bin" / "runners" / "dsh"
    shim.parent.mkdir(parents=True)
    shim.symlink_to(binary)
    runner.argv = ["dsh", "acp"]
    runner.flavor = None
    path_copy = tmp_path / "path" / "dsh"
    path_copy.parent.mkdir()
    path_copy.write_text(f"#!{sys.executable}\nprint('99.0.0')\n")
    path_copy.chmod(0o755)
    monkeypatch.setenv("PATH", str(path_copy.parent))
    monkeypatch.setattr(lina_runners, "ACP_PERMISSION_OBSERVATIONS",
                        {"dsh": {"0.2.1-rc.5": False, "99.0.0": True}}, raising=False)
    supported, reason = runner.honours("workspace-write")
    assert supported is False
    assert "0.2.1-rc.5" in reason


def test_full_follow_up_cannot_narrow_to_unobserved_mode(tmp_path):
    runner, task, _ = make_runner(tmp_path)
    handle = runner.start(task, "write outside", None, access="full")
    try:
        runner.result(handle)
        with pytest.raises(RunnerError, match="dsh.*read-only"):
            runner.follow_up(handle, "continue read-only", access="read-only")
        assert not task.path("acp-inbox.2").exists()
    finally:
        runner.stop(handle)
