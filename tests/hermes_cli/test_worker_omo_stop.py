"""Cancel must remove persistent command watchers before a session reopens (#228)."""

import json

from hermes_cli.worker.runners.base import Handle, Task
from hermes_cli.worker.runners.omo import OmoRunner


def test_stop_removes_restartable_commands_but_keeps_file_watchers(tmp_path, monkeypatch):
    monkeypatch.setattr("hermes_cli.worker.runners.omo_processes._process_table", dict)
    task = Task("OT-CANCEL", tmp_path / "task")
    task.dir.mkdir()
    session_dir = tmp_path / "session"
    terminal = session_dir / "extensions" / "terminal" / "session-test.json"
    terminal.parent.mkdir(parents=True)
    terminal.write_text(json.dumps({"monitors": [
        {"monitorId": "command", "runtimeKind": "command", "durabilityClass": "restartable-command"},
        {"monitorId": "file", "runtimeKind": "path"}], "backgroundSessions": []}))
    task.path("session.json").write_text(json.dumps({
        "durableSessionId": "session-test", "sessionPath": str(session_dir / "session.jsonl")}))
    runner = OmoRunner("omo", {"bin": "fake", "node": "fake", "engine": "fake"}, root=tmp_path)
    calls = []

    def session(args, timeout):
        calls.append(args[0])
        return {"action": "state", "state": {"lifecycle": {"state": "cancelled", "waitingOn": {}}}}

    monkeypatch.setattr(runner, "session", session)
    runner.stop(Handle(task, 1, {"ref": "session-test"}))
    assert [m["monitorId"] for m in json.loads(terminal.read_text())["monitors"]] == ["file"]
    assert "unload" in calls
