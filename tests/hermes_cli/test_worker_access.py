"""Worker access boundaries, settings and protocol declarations (lina#220)."""

import json

import pytest

from hermes_cli.lina_work_settings import ACCESS, validate
from hermes_cli.worker import device, protocol
from hermes_cli.worker.runners.base import Handle, RunnerError, Task
from hermes_cli.worker.task import read_json, write_json


def test_access_args_settings_accept_known_modes_and_reject_invalid_values():
    spec = {"type": "command", "argv": ["fake"], "access_args": {"read-only": ["--read"], "full": []}}
    doc = {"worker": {"runners": {"fake": spec}}}
    assert validate(doc) == []
    for value in ({"unknown": []}, {"full": "--full"}, {"full": [1]}, []):
        spec["access_args"] = value
        assert any(error["path"].startswith("worker.runners.fake.access_args") for error in validate(doc))


def test_every_dispatched_verb_has_an_effect_and_hello_advertises_access(tmp_path):
    table = protocol.verbs()
    assert all(verb.effect in ("read", "write", "destructive") for verb in table.values())
    code, data = device.dispatch(tmp_path / "worker", ["hello"])
    hello = json.loads(data)
    assert code == 0
    assert hello["capabilities"]["access_modes"] == list(ACCESS)
    assert set(hello["capabilities"]["verbs"]) == set(table)


def _task(tmp_path, access):
    root = tmp_path / "worker"
    task = Task("OT-TEST1106000001", root / "tasks" / "OT-TEST1106000001")
    task.dir.mkdir(parents=True)
    write_json(task.path("job.json"), {"access": access, "runner": "fake",
                                      "runner_spec": {"type": "command", "argv": ["fake"],
                                                      "access_args": {mode: [] for mode in ACCESS}}})
    write_json(task.path("grant.json"), {"access": access, "push": False})
    task.path("prompt.1").write_text("first")
    task.path("request.1").write_text('{"run": 1}')
    return root, task


@pytest.mark.parametrize("current", ["read-only", "workspace-write"])
def test_device_rejects_follow_up_escalation_before_writing(tmp_path, current):
    root, task = _task(tmp_path, current)
    before = task.path("job.json").read_bytes()
    reply = device.follow_up(root, task.id, {"access": "full", "message": "more", "prompt": "more"})
    assert reply["action"] == "error"
    assert task.path("job.json").read_bytes() == before
    assert task.path("prompt.1").read_text() == "first"
    assert not task.path("request.2").exists()


def test_device_lower_mode_is_durable_for_next_run_and_grant(tmp_path):
    root, task = _task(tmp_path, "full")
    task.path("rc.1").write_text("0")
    reply = device.follow_up(root, task.id, {"access": "read-only", "message": "more", "prompt": "more"})
    assert reply == {"action": "requested", "run": 2}
    assert read_json(task.path("job.json"))["access"] == "read-only"
    assert read_json(task.path("grant.json")) == {"access": "read-only", "push": False}
    assert device.follow_up(root, task.id, {"access": "full", "message": "bad", "prompt": "bad"})["action"] == "error"


def test_device_refuses_unsupported_mode_before_workspace_preparation(tmp_path):
    root, task = _task(tmp_path, "full")
    write_json(task.path("job.json"), {"access": "full", "runner": "fake",
                                      "runner_spec": {"type": "command", "argv": ["fake"], "access": "read-only"}})
    with pytest.raises(RunnerError):
        device.prepare(root, task)
    assert not task.work.exists()


@pytest.mark.parametrize("access", ACCESS)
def test_repository_push_url_remains_blocked_for_every_mode(tmp_path, monkeypatch, access):
    import subprocess

    class ModeRunner:
        def honours(self, mode):
            return mode in ACCESS, "unknown mode"

        def start(self, task, prompt, model, *, access):
            return Handle(task, 1, {"access": access})

    source = tmp_path / "source"
    source.mkdir()
    for args in (("init", "-q"), ("-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                                 "commit", "--allow-empty", "-qm", "base")):
        subprocess.run(["git", *args], cwd=source, check=True, capture_output=True)
    root, task = _task(tmp_path, access)
    job = read_json(task.path("job.json"))
    job.update(repo=str(source), branch="omo/access-test")
    write_json(task.path("job.json"), job)
    monkeypatch.setattr(device, "_repo_source", lambda worker_root, repo: source)
    device.prepare(root, task)
    assert device._start_or_follow(ModeRunner(), task, 1, job).data["access"] == access
    result = subprocess.run(["git", "config", "--get", "remote.origin.pushurl"], cwd=task.work,
                            capture_output=True, text=True, check=True)
    assert result.stdout.strip() == "PUSH-BLOCKED-by-lina-worker"
    # The sibling/source repository must not inherit the task's block.
    assert subprocess.run(["git", "config", "--get", "remote.origin.pushurl"], cwd=source,
                          capture_output=True, check=False).returncode == 1


@pytest.mark.parametrize("question", [False, True])
def test_live_downgrade_keeps_current_handle_and_next_run_lower(tmp_path, monkeypatch, question):
    root, task = _task(tmp_path, "workspace-write")
    task.path("taken.1").touch()
    Handle(task, 1, {"access": "workspace-write"}).save()
    write_json(task.path("live.1.json"), {"state": "waiting_human" if question else "running"})
    seen = []

    class LiveRunner:
        def honours(self, access):
            return True, ""

        def follow_up(self, handle, text):
            seen.append(handle.data["access"])
            return "steered", handle

        def answer(self, handle, text):
            seen.append(handle.data["access"])
            return {"action": "answer", "steps": 1}

    monkeypatch.setattr(device, "alive", lambda *args: True)
    monkeypatch.setattr(device, "runner_for", lambda *args: LiveRunner())
    reply = device.follow_up(root, task.id, {"access": "read-only", "message": "continue", "prompt": "continue"})
    assert reply["action"] == ("answer" if question else "steered")
    assert seen == ["workspace-write"]
    assert Handle.load(task).data["access"] == "workspace-write"
    assert read_json(task.path("job.json"))["access"] == "read-only"
