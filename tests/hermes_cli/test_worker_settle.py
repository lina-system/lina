"""An omo run the worker ends on its own (lina#35, review of #128): past ``hard_timeout_s`` the session is aborted and
confirmed stopped before the run counts as ended, so no agent keeps working in the worktree after its slot is freed."""

import json
import time

from hermes_cli.worker.runners.base import Task
from hermes_cli.worker.runners.omo_settle import Settle

RUNNING = {"action": "state", "state": {"lifecycle": {"state": "running", "waitingOn": {}}, "isStreaming": True}}
STOPPED = {"action": "state", "state": {"lifecycle": {"state": "cancelled", "waitingOn": {}}, "isStreaming": False}}


class FakeSession:
    def __init__(self, obeys_abort: bool, unreadable: bool = False):
        self.obeys_abort, self.aborted, self.calls, self.unreadable = obeys_abort, False, [], unreadable

    def __call__(self, argv: list[str], timeout: float) -> dict:
        self.calls.append(argv[0])
        if argv[0] == "abort":
            self.aborted = self.obeys_abort
            return {"action": "abort"}
        if argv[0] == "state":
            if self.unreadable:
                return None
            return STOPPED if self.aborted else RUNNING
        if argv[0] == "read":
            return {"action": "read", "entries": []}
        return {"action": argv[0]}


def _expired(tmp_path, session: FakeSession) -> Settle:
    task = Task("OT-SIO1102120020", tmp_path)
    task.path("settle.1.json").write_text(json.dumps({"start": time.time() - 100, "quiet_since": None,
                                                      "last_key": None, "errs": 0, "wakes": 0}))
    return Settle(task, 1, "s-1", session, {"hard_timeout_s": 10, "abort_wait_s": 0})


def test_a_timed_out_session_is_aborted_and_seen_stopped_before_the_run_ends(tmp_path):
    session = FakeSession(obeys_abort=True)
    assert _expired(tmp_path, session).step() == "failed"
    assert "abort" in session.calls
    assert session.calls.index("state", session.calls.index("abort")) > session.calls.index("abort")
    assert json.loads((tmp_path / "wait.1.json").read_text())["outcome"] == "timeout"


def test_a_session_still_working_after_abort_keeps_the_run_and_is_aborted_again(tmp_path):
    session = FakeSession(obeys_abort=False)
    assert _expired(tmp_path, session).step() == "running"
    assert not (tmp_path / "wait.1.json").exists()
    session.obeys_abort = True
    assert _expired(tmp_path, session).step() == "failed"
    assert session.calls.count("abort") == 2
    assert json.loads((tmp_path / "wait.1.json").read_text())["outcome"] == "timeout"


def test_a_session_whose_state_stays_unreadable_is_aborted_and_held_until_seen_stopped(tmp_path):
    session = FakeSession(obeys_abort=True, unreadable=True)
    settle = Settle(Task("OT-SIO1102120021", tmp_path), 1, "s-1", session, {"abort_wait_s": 0})
    assert [settle.step() for _ in range(8)] == ["running"] * 8
    assert "abort" in session.calls
    assert not (tmp_path / "wait.1.json").exists()
    session.unreadable = False
    assert settle.step() == "failed"
    assert json.loads((tmp_path / "wait.1.json").read_text())["outcome"] == "error"
