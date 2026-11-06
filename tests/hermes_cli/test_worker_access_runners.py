"""Runner access contracts for #220, without a real agent, daemon or credentials."""

import json
from pathlib import Path
import shutil
import subprocess
import sys
from typing import Literal, assert_never

import pytest

from hermes_cli.lina_work_settings import ACCESS
from hermes_cli.worker.runners.base import Handle, ModelProfile, RunnerError, Task
from hermes_cli.worker.runners.command import CommandRunner
from hermes_cli.worker.runners.omo import OmoRunner
from tests.hermes_cli import _worker_fakes as fakes


@pytest.fixture
def task(tmp_path: Path) -> Task:
    task = Task("access-contract", tmp_path / "task")
    task.work.mkdir(parents=True)
    return task


@pytest.mark.parametrize("mode", ACCESS)
def test_command_honours_only_declared_access(mode):
    # Given a CLI with no selectable access flags.
    runner = CommandRunner("cli", {"argv": ["true"], "access": mode})
    # When its capabilities are queried.
    supported = {value: runner.honours(value)[0] for value in (*ACCESS, "invalid")}
    # Then only the declared mode is supported.
    assert supported == {value: value == mode for value in (*ACCESS, "invalid")}


def test_command_without_access_contract_refuses_before_launch(task):
    # Given a legacy CLI configuration.
    runner = CommandRunner("cli", {"argv": ["true"]})
    # When starting with the default access.
    with pytest.raises(RunnerError):
        runner.start(task, "work", None)
    # Then no input or handle was published.
    assert not list(task.dir.glob("input.*"))
    assert not task.path("handle.json").exists()


@pytest.mark.parametrize("mapping", [{"full": "--unsafe"}, {"invalid": []}, {"full": [1]}, []])
def test_command_rejects_malformed_access_args(mapping):
    # Given an invalid access mapping, when building the runner, then it refuses.
    with pytest.raises(RunnerError):
        CommandRunner("cli", {"argv": ["true"], "access_args": mapping})


@pytest.fixture
def command(task, monkeypatch):
    log = task.path("argv.jsonl")
    script = 'import json,sys; open(sys.argv[1], "a").write(json.dumps(sys.argv[2:])+"\\n")'
    runner = CommandRunner("cli", {
        "argv": [sys.executable, "-c", script, str(log), "start"],
        "follow_up_argv": [sys.executable, "-c", script, str(log), "continue"],
        "access_args": {mode: ["--sandbox", mode] for mode in ACCESS},
        "env": {"HOME": str(task.dir)},
    })
    processes = []
    popen = subprocess.Popen

    def capture(*args, **kwargs):
        proc = popen(*args, **kwargs)
        processes.append(proc)
        return proc

    monkeypatch.setattr("hermes_cli.worker.runners.command.subprocess.Popen", capture)
    yield runner, processes, log
    for proc in processes:
        proc.communicate(timeout=10)


@pytest.mark.parametrize("mode", ACCESS)
@pytest.mark.parametrize("action", ["start", "follow_up", "answer"])
def test_command_appends_access_flags_on_every_launch(
    task, command, mode, action: Literal["start", "follow_up", "answer"],
):
    # Given a saved mode and a CLI whose argv is observable.
    runner, processes, log = command
    handle = Handle(task, 1, {"access": mode})
    handle.save()
    task.path("exit.1.1").write_text("0")
    (task.work / "question.md").write_text("Which colour?")
    # When launching through one of the three entry points.
    match action:
        case "start":
            runner.start(task, "work", None, mode)
        case "follow_up":
            runner.follow_up(Handle.load(task), "more", run=3)
        case "answer":
            runner.answer(Handle.load(task), "blue")
        case unreachable:
            assert_never(unreachable)
    processes[-1].communicate(timeout=10)
    # Then the mode reaches the real child and survives handle reload.
    assert json.loads(log.read_text())[-2:] == ["--sandbox", mode]
    assert Handle.load(task).data["access"] == mode


@pytest.fixture
def omo(task, tmp_path, monkeypatch):
    node = shutil.which("node")
    if node is None:
        pytest.skip("Node is required for the RPC helper")
    fake = tmp_path / "fake"
    spec = fakes.install_fake_omo(fake, node)
    (fake / "scenario.json").write_text(json.dumps({"first": "question"}))
    monkeypatch.setenv("FAKE_OMO_DIR", str(fake))
    monkeypatch.setenv("LINA_WORKER_ANSWER_PAUSE_MS", "0")
    runner = OmoRunner("omo", spec, root=tmp_path)
    runner.env.update(HOME=str(tmp_path), OMO_CODING_AGENT_DIR=str(tmp_path / "agent"))
    return runner, fake


@pytest.mark.parametrize(("mode", "preset"), zip(ACCESS, ("read-only", None, None)))
@pytest.mark.parametrize("model", [None, ModelProfile("proxy", "model-x", "high")])
def test_omo_sets_session_permission_with_or_without_model(task, omo, mode, preset, model):
    # Given an isolated RPC engine and optional explicit model.
    runner, fake = omo
    # When opening a session.
    handle = runner.start(task, "work", model, mode)
    # Then permission is a session RPC field, never a daemon setting.
    opened = json.loads((fake / "state.json").read_text())["opened"]
    assert {key: value for key, value in opened.items() if key == "permissionPreset"} == (
        {"permissionPreset": preset} if preset else {})
    assert opened.get("modelId") == (model.id if model else None)
    assert Handle.load(task).data["access"] == handle.data["access"] == mode
    assert runner.honours(mode) == (True, "")


@pytest.mark.parametrize("action", ["follow_up", "answer"])
@pytest.mark.parametrize("mode", ACCESS)
def test_omo_restores_recorded_permission_before_continuation(
    task, omo, action: Literal["follow_up", "answer"], mode,
):
    # Given a recorded session mode.
    runner, fake = omo
    handle = runner.start(task, "work", None, mode)
    # When continuing or answering through the runner.
    match action:
        case "follow_up":
            runner.follow_up(Handle.load(task), "more", run=2)
        case "answer":
            runner.answer(Handle.load(task), "blue")
        case unreachable:
            assert_never(unreachable)
    # Then every attach, including permission inspection and answer, preserves the mapping.
    opens = [json.loads(line)["openSession"] for line in (fake / "rpc.jsonl").read_text().splitlines()
             if "openSession" in json.loads(line)]
    expected = {"sessionPath": "/fake/ses.jsonl", "cwd": str(task.work)}
    if mode == "read-only":
        expected["permissionPreset"] = "read-only"
    assert opens[1:] and all(opened == expected for opened in opens[1:])
    assert Handle.load(task).data["access"] == handle.data["access"]


@pytest.mark.parametrize("kind", ["command", "omo"])
@pytest.mark.parametrize("requested", ["workspace-write", "full", "invalid"])
def test_follow_up_rejects_access_change_before_any_side_effect(task, command, omo, kind, requested):
    # Given a persisted read-only handle, including a live command PID.
    runner = {"command": command[0], "omo": omo[0]}[kind]
    handle = Handle(task, 1, {"access": "read-only", "pid": 1, "ref": "ses-fake-1"})
    handle.save()
    before = set(task.dir.iterdir())
    # When a continuation asks for a different mode.
    with pytest.raises(RunnerError):
        runner.follow_up(Handle.load(task), "more", access=requested)
    # Then the session receives nothing and retains its recorded mode.
    assert set(task.dir.iterdir()) == before
    assert Handle.load(task).data["access"] == "read-only"


def test_omo_default_access_is_workspace_write(task, omo):
    # Given no model and no access override, task access is workspace without an engine preset.
    runner, fake = omo
    handle = runner.start(task, "work", None)
    assert "permissionPreset" not in json.loads((fake / "state.json").read_text())["opened"]
    assert handle.data["access"] == "workspace-write"


@pytest.mark.parametrize("kind", ["command", "omo"])
@pytest.mark.parametrize("recorded", [None, "invalid"])
def test_unrecorded_access_refuses_continuation(task, command, omo, kind, recorded):
    # Given a legacy or corrupt handle, when continuing, then no access is guessed.
    runner = {"command": command[0], "omo": omo[0]}[kind]
    handle = Handle(task, 1, {"ref": "ses-fake-1", "access": recorded})
    with pytest.raises(RunnerError, match="recorded access"):
        runner.follow_up(handle, "more")


def test_omo_invalid_mode_refuses_before_daemon_call(task, omo):
    # Given an unknown mode, when starting, then no daemon or helper is invoked.
    runner, fake = omo
    with pytest.raises(RunnerError):
        runner.start(task, "work", None, "invalid")
    assert not (fake / "calls.jsonl").exists()


@pytest.mark.parametrize("kind", ["command", "omo"])
def test_follow_up_records_explicit_downgrade(task, command, omo, kind):
    # Given a workspace session, when narrowed, then the next run records read-only.
    runner = {"command": command[0], "omo": omo[0]}[kind]
    handle = Handle(task, 1, {"access": "workspace-write", "ref": "ses-fake-1"})
    if kind == "omo":
        handle = runner.start(task, "work", None)
    _, new = runner.follow_up(handle, "more", run=2, access="read-only")
    assert new.data["access"] == Handle.load(task).data["access"] == "read-only"


def test_omo_failed_downgrade_does_not_mutate_handle(task, omo, monkeypatch):
    # Given a recorded mode and a failed attach, when narrowed, then the record is unchanged.
    runner, _ = omo
    handle = runner.start(task, "work", None)
    monkeypatch.setattr(runner, "_call", lambda *args: ({"action": "error"}, 1))
    with pytest.raises(RunnerError):
        runner.follow_up(handle, "more", access="read-only")
    assert handle.data["access"] == Handle.load(task).data["access"] == "workspace-write"
