"""Hub access propagation and monotone follow-ups (issue #220)."""

import argparse
import json
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli.subcommands import worker as cli
from hermes_cli.worker import hub as hub_mod, notice, settings
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.transport import Reply


@pytest.fixture
def lane(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setattr(settings, "section", lambda base: {})
    monkeypatch.setattr(hub_mod.runners, "build", lambda *args: SimpleNamespace(honours=lambda mode: (True, "")))
    lines = []
    hub = Hub(tmp_path / "worker", out=lines.append)
    brief = tmp_path / "brief.md"
    brief.write_text("Do the task", encoding="utf-8")
    opts = {"bot": "sion", "topic": "access", "mode": "ulw", "inputs": []}
    return hub, brief, opts, lines


def parser():
    root = argparse.ArgumentParser()
    cli.build_worker_parser(root.add_subparsers())
    return root


@pytest.mark.parametrize("access", [None, "read-only", "full"])
def test_submit_cli_propagates_access(lane, monkeypatch, access):
    # Given a CLI bound to an isolated hub, without placement or external calls.
    hub, brief, opts, _ = lane
    hub.worker["access"] = {"sion": "full"}
    monkeypatch.setattr(cli, "_hub", lambda: hub)
    monkeypatch.setattr(hub, "submit", lambda supplied: hub.make_job(brief, {**opts, **supplied}, None))
    argv = ["worker", "submit", "--brief", str(brief)]
    if access is not None:
        argv += ["--access", access]
    # When the real parser and submit adapter dispatch the command.
    job = cli._submit(parser().parse_args(argv))
    # Then the persisted job, grant and receipt carry the selected mode.
    expected = access or "workspace-write"
    saved = hub.need(job["id"])
    receipt, = hub.store.receipts_of(job["id"])
    assert (saved["access"], saved["grant"]["access"], receipt["access"]) == (expected,) * 3


@pytest.mark.parametrize("ceiling, requested", [(None, "full"), ("read-only", "workspace-write"),
                                                ("workspace-write", "full")])
def test_submit_rejects_access_above_agent_ceiling(lane, ceiling, requested):
    # Given the requesting agent's ceiling, not another agent's full grant.
    hub, brief, opts, _ = lane
    hub.worker["access"] = {"other": "full", **({"sion": ceiling} if ceiling else {})}
    # When access exceeds that ceiling.
    with pytest.raises(HubError, match="ceiling"):
        hub.make_job(brief, {**opts, "access": requested}, None)
    # Then no job or submitted receipt is published.
    assert hub.store.all() == []
    assert not hub.store.receipts.exists()


def test_submit_preflights_runner_access_support(lane, monkeypatch):
    # Given a known runner unable to honour the requested mode.
    hub, brief, opts, _ = lane
    checked = []
    monkeypatch.setattr(hub_mod.runners, "build", lambda *args: SimpleNamespace(
        honours=lambda mode: (bool(checked.append(mode)), "cannot honour requested access")))
    # When submission preflights the runner.
    with pytest.raises(HubError, match="cannot honour"):
        hub.make_job(brief, opts, None)
    # Then the requested mode was checked before persistence.
    assert checked == ["workspace-write"]
    assert hub.store.all() == []


def test_publish_and_resume_carry_recorded_access(lane, monkeypatch):
    # Given a persisted job and an in-memory publication transport.
    hub, brief, opts, _ = lane
    job = hub.make_job(brief, {**opts, "access": "read-only"}, None)
    published = {}
    transport = SimpleNamespace(call=lambda *args, **kw: Reply(0, b'{}', ""))
    monkeypatch.setattr(hub_mod, "for_device", lambda *args: transport)
    monkeypatch.setattr(hub_mod, "put", lambda tr, tid, name, data: published.update({name: data}))
    # When placement resumes after a hub restart.
    Hub(hub.worker_root)._publish(hub.need(job["id"]), settings.Device("here", "local"), resume=True)
    # Then both device records have the durable mode.
    assert json.loads(published["job.json"])["access"] == "read-only"
    assert json.loads(published["grant.json"])["access"] == "read-only"


@pytest.fixture
def sending(lane, monkeypatch):
    hub, brief, opts, _ = lane
    job = hub.make_job(brief, opts, None)
    hub.store.update(job["id"], host="here", state="done")
    state = {"alive": False, "human": False, "action": "requested"}
    calls = []

    def call(argv, stdin=b"", timeout=180):
        calls.append((argv[0], stdin))
        reply = {"open": state["human"]} if argv[0] == "human" else {
            "action": state["action"], "run": 2, "steps": 1}
        return Reply(0, json.dumps(reply).encode(), "")

    monkeypatch.setattr(hub, "transport", lambda job: SimpleNamespace(call=call))
    monkeypatch.setattr(hub, "meta", lambda job, **kw: {"alive": state["alive"], "run": 1})
    monkeypatch.setattr(hub_mod.kanban_link, "card_from_env", lambda: None)
    return hub, job["id"], state, calls


@pytest.mark.parametrize("action", ["requested", "queued"])
def test_send_cli_lowers_next_run_durably(sending, monkeypatch, action):
    # Given an ended or untaken queued run.
    hub, tid, state, calls = sending
    state["action"] = action
    monkeypatch.setattr(cli, "_hub", lambda: hub)
    args = parser().parse_args(["worker", "send", tid, "continue", "--access", "read-only"])
    # When the CLI requests a lower next-run mode.
    cli._ACTIONS["send"](args)
    # Then the wire payload and restarted hub agree, and escalation cannot follow.
    payload = json.loads(dict(calls)["follow_up"])
    restarted = Hub(hub.worker_root)
    saved = restarted.need(tid)
    assert payload["access"] == saved["access"] == saved["grant"]["access"] == "read-only"
    with pytest.raises(HubError, match="recorded access"):
        restarted.send(tid, "escalate", access="workspace-write")


@pytest.mark.parametrize("human, action", [(False, "steered"), (True, "answer")])
def test_send_live_downgrade_applies_to_next_run(sending, human, action):
    # Given a live run, including one waiting for an answer.
    hub, tid, state, calls = sending
    state.update(alive=True, human=human, action=action)
    # When a downgrade is requested.
    hub.send(tid, "continue", access="read-only")
    # Then the device receives the downgrade with the message, not a separate answer or steer.
    assert [verb for verb, _ in calls] == ["human", "follow_up"]
    assert json.loads(calls[-1][1])["access"] == "read-only"
    saved = hub.need(tid)
    assert saved["access"] == saved["grant"]["access"] == "read-only"


def test_send_busy_race_keeps_recorded_access(sending):
    # Given a device that took the run after the hub observed it idle.
    hub, tid, state, _ = sending
    state["action"] = "busy"
    # When the device refuses the downgrade under its run lock.
    with pytest.raises(HubError, match="starting or running"):
        hub.send(tid, "continue", access="read-only")
    # Then the hub does not claim a downgrade the device refused.
    assert hub.need(tid)["access"] == "workspace-write"


def test_answer_keeps_recorded_access(sending):
    # Given a question in a live run.
    hub, tid, state, calls = sending
    state.update(alive=True, human=True, action="answer")
    # When the answer uses the unchanged access mode.
    hub.send(tid, "blue")
    # Then the answer remains plain text in the same run and preserves access.
    assert calls[-1] == ("answer", b"blue")
    assert hub.need(tid)["access"] == "workspace-write"


def test_legacy_send_defaults_to_workspace_write(sending):
    # Given a job written before access was recorded.
    hub, tid, _, calls = sending
    with hub.store.edit(tid) as job:
        job.pop("access")
        job["grant"].pop("access")
    # When another run is requested without an override.
    hub.send(tid, "continue")
    # Then the device gets the legacy default explicitly.
    assert json.loads(calls[-1][1])["access"] == "workspace-write"


def test_send_rejects_escalation_before_transport(sending):
    # Given a recorded workspace-write grant.
    hub, tid, _, calls = sending
    # When full access is requested for a continuation.
    with pytest.raises(HubError, match="recorded access"):
        hub.send(tid, "continue", access="full")
    # Then neither an answer nor any device request can carry the escalation.
    assert calls == []


def test_status_and_notice_headlines_show_access(lane):
    # Given a queued task with recorded read-only access.
    hub, brief, opts, lines = lane
    job = hub.make_job(brief, {**opts, "access": "read-only"}, None)
    # When status and an end notice are rendered.
    hub.status(job["id"])
    text = notice.body(job, "cancelled", "1", brief.parent, {})
    # Then the mode is visible on each first line.
    assert "read-only" in lines[0]
    assert "read-only" in text.splitlines()[0]


def test_live_downgrade_notice_keeps_original_run_mode(sending):
    hub, tid, state, _ = sending
    state.update(alive=True, action="steered")
    hub.send(tid, "continue", access="read-only")
    saved = hub.need(tid)
    assert saved["access"] == "read-only"
    assert saved["run_access"]["1"] == "workspace-write"
    text = notice.body(saved, "cancelled", "1", hub.worker_root, {})
    assert "workspace-write" in text.splitlines()[0]
