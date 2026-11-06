"""Explicit card identity survives Hermes's delegated-child environment (lina#270)."""

import json
import os
import sys

import pytest

from hermes_cli.worker import device
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.runners.base import Task
from tests.hermes_cli.test_worker_hermes_card import _cli, _config, FAKE_KANBAN


@pytest.fixture
def shell(tmp_path, monkeypatch):
    lina = tmp_path / "lina"
    fake = tmp_path / "kanban.py"
    fake.write_text(FAKE_KANBAN, encoding="utf-8")
    log = tmp_path / "kanban.jsonl"
    _config(lina, [sys.executable, str(fake)])
    brief = tmp_path / "brief.md"
    brief.write_text("Do the work", encoding="utf-8")
    monkeypatch.setenv("LINA_HOME", str(lina))
    monkeypatch.setenv("HERMES_HOME", str(lina))
    monkeypatch.setenv("FAKE_KANBAN_LOG", str(log))
    monkeypatch.setenv("HERMES_DELEGATED_CHILD_CONTEXT", "1")
    monkeypatch.setenv("HERMES_KANBAN_BOARD", "env-board")
    monkeypatch.setenv("HERMES_KANBAN_WORKSPACE", str(tmp_path / "t_not_an_identity"))
    for key in ("HERMES_KANBAN_TASK", "HERMES_PROFILE", "HERMES_PROFILE_NAME"):
        monkeypatch.delenv(key, raising=False)
    return Hub(lina / "worker"), brief, log


def test_delegated_submit_requires_card_before_creating_a_run(shell, capsys):
    hub, brief, log = shell
    assert _cli(["submit", "--brief", str(brief), "--topic", "work"]) == 2
    assert "--card <id> --board <slug>" in capsys.readouterr().err
    assert hub.store.all() == []
    assert not log.exists()


@pytest.mark.parametrize(("profile", "profile_name", "bot", "owner"), [
    ("card-owner", "other-owner", None, "card-owner"),
    (None, "named-owner", None, "named-owner"),
    ("card-owner", None, "explicit-owner", "explicit-owner"),
    (None, None, None, "default"),
])
def test_explicit_card_owner_and_start_comment(shell, monkeypatch, capsys, profile, profile_name, bot, owner):
    hub, brief, log = shell
    for key, value in (("HERMES_PROFILE", profile), ("HERMES_PROFILE_NAME", profile_name)):
        if value:
            monkeypatch.setenv(key, value)
    monkeypatch.setenv("HERMES_KANBAN_TASK", "t_env")
    args = ["submit", "--brief", str(brief), "--card", "t_explicit", "--board", "explicit-board"]
    assert _cli(args + (["--bot", bot] if bot else [])) == 0
    job, = hub.store.all()
    assert job["bot"] == owner
    assert job["card"] == {"task": "t_explicit", "board": "explicit-board", "kind": "hermes"}
    assert owner in capsys.readouterr().out
    assert not log.exists()
    with monkeypatch.context() as hub_env:
        hub_env.delenv("HERMES_DELEGATED_CHILD_CONTEXT")
        hub.tick()
    start, = [json.loads(line) for line in log.read_text().splitlines()]
    assert start["argv"][:5] == ["kanban", "--board", "explicit-board", "comment", "t_explicit"]
    assert "HERMES_DELEGATED_CHILD_CONTEXT" not in start["env"]
    assert _cli(["wait", "--card", "t_explicit", "--board", "explicit-board"]) == 0


def test_send_explicit_card_overrides_env_and_refuses_wrong_card(shell, monkeypatch):
    hub, brief, _ = shell
    assert _cli(["submit", "--brief", str(brief), "--card", "t_explicit", "--board", "explicit-board"]) == 0
    job, = hub.store.all()
    hub.store.update(job["id"], host="fake", state="running")
    monkeypatch.setenv("HERMES_KANBAN_TASK", "t_env")
    messages = []
    monkeypatch.setattr(Hub, "_next_run", lambda self, job, message, **_: messages.append(message) or 0)
    monkeypatch.setattr(Hub, "meta", lambda *args: {"alive": False})

    class Transport:
        def call(self, *args, **kwargs):
            return self

        def json(self):
            return {"open": False}

    monkeypatch.setattr(Hub, "transport", lambda *args: Transport())
    args = ["send", job["id"], "continue", "--board", "explicit-board", "--card"]
    assert _cli([*args, "t_explicit"]) == 0
    assert _cli([*args, "t_wrong"]) == 2
    assert messages == ["continue"]


@pytest.mark.platforms("linux")
def test_systemd_run_receives_resolved_nondefault_lina_home(tmp_path, monkeypatch):
    home = tmp_path / "nondefault"
    root = home / "worker"
    task = Task("OT-test", root / "tasks" / "OT-test")
    task.dir.mkdir(parents=True)
    log = tmp_path / "systemd.json"
    binary = tmp_path / "systemd-run"
    binary.write_text(f"#!{sys.executable}\nimport json, sys\n"
                      f"with open({str(log)!r}, 'w') as f: json.dump(sys.argv[1:], f)\n", encoding="utf-8")
    binary.chmod(0o755)
    monkeypatch.setenv("PATH", str(tmp_path) + os.pathsep + os.environ["PATH"])
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("LINA_HOME", "nondefault")
    device.launch(root, task, 1)
    argv = json.loads(log.read_text())
    assert f"--setenv=LINA_HOME={home.resolve()}" in argv
    assert task.path("unit.1").read_text().strip() == "lina-worker-OT-test-1"
