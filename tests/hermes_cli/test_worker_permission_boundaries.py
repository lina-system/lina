"""Permission dialogs never enter the worker human-answer channel (issue #220)."""

import json
import tempfile
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli.worker import device, hub as hub_mod, notice, settings
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.runners.base import Handle, RunnerError
from hermes_cli.worker.task import read_json, write_json
from hermes_cli.worker.transport import Reply
from tests.hermes_cli import _worker_fakes as fakes


@pytest.fixture
def lane(monkeypatch):
    with tempfile.TemporaryDirectory(prefix=".permission-boundaries-", dir=Path.cwd()) as directory:
        base = Path(directory)
        monkeypatch.setenv("HERMES_HOME", str(base))
        monkeypatch.setattr(Path, "home", lambda: base)
        monkeypatch.setattr(settings, "section", lambda base: {})
        monkeypatch.setattr(device, "alive", lambda *args: True)
        monkeypatch.setattr(hub_mod.kanban_link, "card_from_env", lambda: None)
        root, tid = base / "worker", "OT-PERMISSION"
        task = device.task_of(root, tid)
        task.dir.mkdir(parents=True)
        task.path("prompt.1").write_text("task", encoding="utf-8")
        task.path("request.1").write_text('{"run": 1}', encoding="utf-8")
        task.path("taken.1").touch()
        task.path("state").write_text("running", encoding="utf-8")
        write_json(task.path("live.1.json"), {"state": "waiting_human"})
        write_json(task.path("job.json"), {"runner": "fake", "access": "workspace-write"})
        Handle(task, 1).save()
        calls, forwarded = [], []
        runner = SimpleNamespace(
            permission_pending=lambda handle: True,
            honours=lambda access: (True, ""),
            answer=lambda handle, text: forwarded.append(text) or {"action": "answer", "steps": 1},
            follow_up=lambda handle, text: (forwarded.append(text) or "steered", handle),
        )
        monkeypatch.setattr(device, "runner_for", lambda *args: runner)

        def call(argv, stdin=b"", timeout=90):
            calls.append(argv[0])
            code, output = device.dispatch(root, argv, stdin)
            return Reply(code, output, "")

        hub = Hub(root, out=lambda text: None)
        hub.store.save({"id": tid, "host": "here", "state": "running", "runs": 1,
                        "created": 1,
                        "access": "workspace-write", "grant": {"access": "workspace-write"},
                        "bot": "test", "topic": "permissions", "notified": [], "human_since": 1})
        monkeypatch.setattr(hub, "transport", lambda job: SimpleNamespace(call=call))
        yield hub, task, runner, calls, forwarded


@pytest.mark.parametrize("cached", ["waiting_human", "running"])
def test_human_reports_fresh_permission_instead_of_open_question(lane, cached):
    # Given a real permission dialog, regardless of the cached run state.
    hub, task, _, _, _ = lane
    write_json(task.path("live.1.json"), {"state": cached})
    # When the device human verb is dispatched.
    code, output = device.dispatch(hub.worker_root, ["human", task.id])
    # Then the hub can distinguish it from an answerable question.
    assert code == 0
    assert json.loads(output) == {"open": False, "permission": True, "run": 1}


@pytest.mark.parametrize("verb", ["answer", "send", "follow_up"])
@pytest.mark.parametrize("access", ["workspace-write", "read-only"])
def test_device_refuses_permission_without_forwarding_answer(lane, verb, access):
    # Given a live run with a permission dialog and an attempted human answer.
    hub, task, _, _, forwarded = lane
    text = b"Allow once" if verb != "follow_up" else json.dumps(
        {"message": "Allow once", "prompt": "Allow once", "access": access}).encode()
    # When any human input verb reaches the actual device dispatch.
    code, output = device.dispatch(hub.worker_root, [verb, task.id], text)
    # Then neither a runner input nor a next run or downgrade is published.
    assert code == 1
    assert json.loads(output) == {"action": "error", "reason": "permission_dialog", "run": 1}
    assert forwarded == []
    assert not task.path("request.2").exists()
    assert read_json(task.path("job.json"))["access"] == "workspace-write"


@pytest.mark.parametrize("access", [None, "read-only"])
def test_hub_send_refuses_allow_once_before_answer_or_steer(lane, access):
    # Given the real hub/device wire and an actual pending permission.
    hub, task, _, calls, forwarded = lane
    # When send attempts a permission answer, with or without a downgrade.
    with pytest.raises(HubError, match="permission_dialog"):
        hub.send(task.id, "Allow once", access=access)
    # Then only read-only queries reached the device.
    assert calls == ["meta", "human"]
    assert forwarded == []
    assert hub.need(task.id)["runs"] == 1


def test_hub_answer_permission_race_never_closes_or_starts_next_run(lane):
    # Given a question that becomes a permission dialog after the human query.
    hub, task, runner, calls, forwarded = lane
    checks = iter([False, True])
    runner.permission_pending = lambda handle: next(checks)
    # When send reaches the device answer guard.
    with pytest.raises(HubError, match="permission_dialog"):
        hub.send(task.id, "Allow once")
    # Then the failed answer is not treated as closing the earlier question.
    assert calls == ["meta", "human", "answer"]
    assert forwarded == []
    assert hub.need(task.id)["notified"] == []
    assert not task.path("request.2").exists()


def test_hub_refuses_permission_race_inside_runner_answer(lane):
    # Given the runner's fresh answer check finds a dialog after preflight.
    hub, task, runner, calls, _ = lane
    runner.permission_pending = lambda handle: False

    def refuse(handle, text):
        raise RunnerError("permission_dialog: permissions are decided by the run access mode")

    runner.answer = refuse
    # When send reaches that final runner boundary.
    with pytest.raises(HubError, match="permission_dialog"):
        hub.send(task.id, "Allow once")
    # Then the hub does not close the run or request another one.
    assert calls == ["meta", "human", "answer"]
    assert hub.need(task.id)["notified"] == []


def test_tick_never_delivers_permission_as_human_notice(lane, monkeypatch):
    # Given a stale human dashboard and a notification timer already expired.
    hub, task, _, _, _ = lane
    delivered = []
    monkeypatch.setattr(hub, "_deliver", lambda *args: delivered.append(args) or notice.Sent(True, "delivered"))
    monkeypatch.setattr(hub_mod.time, "time", lambda: 1000)
    # When the real tick checks the live run.
    hub.tick()
    # Then no human notice, receipt or timer survives for this dialog.
    assert delivered == []
    assert hub.need(task.id)["notified"] == []
    assert "human_since" not in hub.need(task.id)
    assert hub.store.receipts_of(task.id) == []


@pytest.mark.parametrize("verb", ["answer", "send"])
def test_runner_without_permission_method_keeps_human_channel(lane, verb):
    # Given a command-style runner with no permission channel.
    hub, task, runner, _, forwarded = lane
    del runner.permission_pending
    # When a normal answer or instruction is sent through device dispatch.
    code, output = device.dispatch(hub.worker_root, [verb, task.id], b"blue")
    # Then the existing runner channel remains usable.
    assert code == 0
    assert json.loads(output)["action"] == ("answer" if verb == "answer" else "steered")
    assert forwarded == ["blue"]


@pytest.mark.parametrize("verb", ["answer", "send", "follow_up"])
def test_permission_query_failure_never_forwards_input(lane, verb):
    # Given an unavailable permission query, not proof that the dialog is absent.
    hub, task, runner, _, forwarded = lane

    def unavailable(handle):
        raise RunnerError("permission query unavailable")

    runner.permission_pending = unavailable
    text = b"Allow once" if verb != "follow_up" else json.dumps(
        {"message": "Allow once", "prompt": "Allow once"}).encode()
    # When the device cannot freshly determine permission state.
    code, output = device.dispatch(hub.worker_root, [verb, task.id], text)
    # Then it fails closed without touching the runner input channel.
    assert code == 1
    assert json.loads(output)["action"] == "error"
    assert forwarded == []


def test_tick_still_delivers_normal_human_question(lane, monkeypatch):
    # Given a command-style runner waiting for a normal question answer.
    hub, task, runner, _, _ = lane
    del runner.permission_pending
    delivered = []
    monkeypatch.setattr(hub, "_deliver", lambda *args: delivered.append(args) or notice.Sent(True, "delivered"))
    monkeypatch.setattr(hub_mod.time, "time", lambda: 1000)
    # When the notification delay has elapsed.
    hub.tick()
    # Then the existing human question notice still arrives.
    assert len(delivered) == 1
    assert fakes.notified(hub.need(task.id)) == ["question:1"]
