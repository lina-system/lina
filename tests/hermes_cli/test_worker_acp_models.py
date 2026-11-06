"""The ACP run records confirmed models, not a submission's unapplied request."""

import json
import sys

import pytest

from hermes_cli.worker.runners.acp import AcpRunner
from hermes_cli.worker.runners.base import Handle, ModelProfile, Task
from hermes_cli.worker.task import write_manifest

AGENT = r'''
import json, sys
from pathlib import Path
scenario, logfile = sys.argv[1:]
current = "default-model"
def send(i, result):
    print(json.dumps({"jsonrpc":"2.0", "id":i, "result":result}), flush=True)
def models():
    return {"currentModelId":current, "availableModels":[{"modelId":"requested-model","name":"Requested"}]}
def config():
    return [{"id":"model","category":"model","type":"select","currentValue":current,
             "options":[{"value":"requested-model","name":"Requested"}]}]
for line in sys.stdin:
    msg=json.loads(line)
    with open(logfile, "a") as f: f.write(json.dumps(msg)+"\n")
    method, i = msg.get("method"), msg.get("id")
    if method == "initialize": send(i, {"protocolVersion":1,"agentCapabilities":{}})
    elif method == "session/new":
        reply={"sessionId":"model-session"}
        if scenario != "unsupported":
            reply.update({"configOptions":config()} if scenario == "config" else {"models":models()})
        send(i, reply)
    elif method in ("session/set_model", "session/set_config_option"):
        if scenario == "rejected":
            print(json.dumps({"jsonrpc":"2.0","id":i,"error":{"code":-32602,"message":"model unavailable"}}), flush=True)
        else:
            current = msg["params"].get("modelId", msg["params"].get("value"))
            send(i, {} if scenario == "unconfirmed" else {"configOptions":config()} if scenario == "config" else {"models":models()})
    elif method == "session/prompt":
        Path("observed-model").write_text(current)
        send(i, {"stopReason":"end_turn"})
'''


def run_model(tmp_path, scenario, source="runner:configured", requested=True):
    task = Task("model-contract", tmp_path)
    task.work.mkdir()
    model = ModelProfile("provider", "requested-model", source=source) if requested else None
    task.path("job.json").write_text(json.dumps({"model": model.as_dict() if model else None}), encoding="utf-8")
    runner = AcpRunner("fake", {"argv":[sys.executable, "-u", "-c", AGENT, scenario, str(task.path("wire.jsonl"))]},
                       {"stall_grace_s": 5})
    handle = runner.start(task, "Reply done.", model)
    try:
        runner._sessions[1].thread.join(timeout=15)
        assert not runner._sessions[1].thread.is_alive()
        return runner.state(handle), Handle.load(task).data, write_manifest(task, 1), task
    finally:
        runner.stop(handle)


@pytest.mark.parametrize("scenario", ["config", "legacy"])
def test_selected_model_is_confirmed_and_recorded_in_handle_and_manifest(tmp_path, scenario):
    state, handle, manifest, task = run_model(tmp_path, scenario, source="--model")
    assert state == "done"
    assert (task.work / "observed-model").read_text(encoding="utf-8-sig") == "requested-model"
    assert handle["model"]["id"] == manifest["model"]["id"] == "requested-model"
    assert handle["requested_model"]["id"] == manifest["requested_model"]["id"] == "requested-model"


@pytest.mark.parametrize("scenario", ["unsupported", "rejected", "unconfirmed"])
@pytest.mark.parametrize("source,state", [("runner:configured", "done"), ("--model", "failed")])
def test_unapplied_model_is_null_and_explicit_requests_fail_before_prompt(tmp_path, scenario, source, state):
    actual, handle, manifest, task = run_model(tmp_path, scenario, source=source)
    assert actual == state
    assert handle["model"] is None and manifest["model"] is None
    assert handle["requested_model"]["id"] == manifest["requested_model"]["id"] == "requested-model"
    events = [json.loads(line) for line in task.path("events.1.jsonl").read_text(encoding="utf-8-sig").splitlines()]
    denied = [event for event in events if event["kind"] == "model_not_applied"]
    assert len(denied) == 1 and denied[0]["requested_model"]["id"] == "requested-model" and denied[0]["reason"]
    assert (task.work / "observed-model").exists() == (source != "--model")


def test_no_requested_model_runs_without_model_selection(tmp_path):
    state, handle, manifest, task = run_model(tmp_path, "unsupported", requested=False)
    assert state == "done" and handle["model"] is None and manifest["model"] is None
    assert (task.work / "observed-model").read_text(encoding="utf-8-sig") == "default-model"
    assert "model_not_applied" not in task.path("events.1.jsonl").read_text(encoding="utf-8-sig")


def test_manifest_does_not_take_another_runs_model(tmp_path):
    task = Task("run-boundary", tmp_path)
    task.work.mkdir()
    task.path("job.json").write_text(json.dumps({"model":{"id":"job-model"}}), encoding="utf-8")
    Handle(task, 2, {"model":{"id":"other-run"}, "requested_model":{"id":"requested-model"}}).save()
    assert write_manifest(task, 1)["model"] == {"id":"job-model"}
