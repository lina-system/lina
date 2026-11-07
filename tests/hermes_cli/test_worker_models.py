"""The model profile of a ``lina worker`` run (lina#35): chosen per run, defaulting from the mode in config, overridable
with ``--model``, recorded with the run; for omo it reaches the daemon's open_session through ``omo_open.mjs`` on Node
with provider, model id and thinking level apart. Also the omo answer fallback: when no answer path takes the answer,
the run closes on its question and the answer goes to the next run."""

import json
import shutil

import pytest

from hermes_cli.worker import models, serve
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.runners.base import RunnerError
from tests.hermes_cli import _worker_fakes as fakes

WORKER = {"models": {"aliases": {"planner": {"provider": "proxy", "id": "vendor/plan-model", "thinking": "medium"},
                                 "builder": {"provider": "proxy", "id": "build-model", "thinking": "high"}},
                     "defaults": {"research": "planner", "ulw": "builder"}}}
NODE = shutil.which("node")


@pytest.fixture(autouse=True)
def isolated_process_table(monkeypatch):
    monkeypatch.setattr("hermes_cli.worker.runners.omo_processes._process_table", dict)


def test_the_mode_picks_the_default_and_model_overrides_it():
    assert models.for_run("research", None, WORKER).as_dict() == {
        "provider": "proxy", "id": "vendor/plan-model", "thinking": "medium", "alias": "planner",
        "source": "mode:research"}
    assert models.for_run("mass", None, WORKER) is None
    assert models.for_run("research", "builder", WORKER).alias == "builder"
    raw = models.for_run("ulw", "other/vendor/model-x:xhigh", WORKER)
    assert (raw.provider, raw.id, raw.thinking, raw.source) == ("other", "vendor/model-x", "xhigh", "--model")
    assert models.for_run("ulw", "proxy/vendor/plan-model", WORKER).alias == "planner"
    with pytest.raises(RunnerError):
        models.for_run("ulw", "no-provider", WORKER)


def _omo_lane(tmp_path, monkeypatch, defaults: str):
    if NODE is None:
        pytest.skip("the omo helpers run on Node, and no node is on PATH")
    base, fake = tmp_path / "lina", tmp_path / "fake"
    spec = fakes.install_fake_omo(fake, NODE)
    monkeypatch.setenv("FAKE_OMO_DIR", str(fake))
    monkeypatch.setenv("LINA_WORKER_ANSWER_PAUSE_MS", "0")
    fakes.write_config(base, f"omo: {json.dumps(spec)}\n", extra=defaults)
    launcher = fakes.ThreadLauncher().install(monkeypatch)
    serve.serve_once(base / "worker")
    return base / "worker", fake, launcher


def test_an_omo_run_opens_with_the_mode_default_and_records_it(tmp_path, monkeypatch):
    root, fake, launcher = _omo_lane(tmp_path, monkeypatch, "  models:\n    aliases:\n      planner: {provider: proxy, "
                                     "id: vendor/plan-model, thinking: medium}\n    defaults: {research: planner}\n")
    (fake / "scenario.json").write_text(json.dumps({"first": "done"}))
    lines: list[str] = []
    hub = Hub(root, out=lines.append)
    hub.submit({"briefs": [fakes.brief(tmp_path, "look it up")], "mode": "research", "topic": "m", "bot": "sion",
                "wait": None})
    task_id = lines[0].split()[0]
    assert "model planner" in lines[0]
    serve.serve_once(root)
    fakes.until(lambda: "done:1" in hub.store.load(task_id)["notified"], what="the run ended")
    launcher.join()
    opened = [json.loads(line)["openSession"] for line in (fake / "rpc.jsonl").read_text().splitlines()
              if "openSession" in line]
    assert opened == [{**opened[0], "provider": "opencodex", "modelId": "proxy/vendor/plan-model",
                       "thinkingLevel": "medium"}]
    assert json.loads((fake / "state.json").read_text())["prompts"][0].startswith("ulw research look it up")
    manifest = fakes.as_json(hub.store.out_dir(hub.store.load(task_id)) / "manifest.json")
    assert manifest["model"]["alias"] == "planner" and manifest["model"]["source"] == "mode:research"


def test_an_answer_no_path_takes_closes_the_run_and_goes_to_the_next(tmp_path, monkeypatch):
    root, fake, launcher = _omo_lane(tmp_path, monkeypatch, "")
    monkeypatch.setenv("FAKE_ANSWER_REFUSED", "1")
    (fake / "scenario.json").write_text(json.dumps({"first": "question"}))
    lines: list[str] = []
    hub = Hub(root, out=lines.append)
    hub.submit({"briefs": [fakes.brief(tmp_path, "ask first")], "mode": "ulw", "topic": "a", "bot": "sion",
                "wait": None})
    task_id = lines[0].split()[0]
    serve.serve_once(root)
    fakes.until(lambda: hub.transport(hub.store.load(task_id)).call(["human", task_id]).json().get("open"),
                what="the run waits on a person")
    assert hub.send(task_id, "green") == 0
    assert "started run 2" in lines[-1]
    job = hub.store.load(task_id)
    assert "stalled:1" in job["notified"]
    assert not list(hub.store.notices.glob(f"{task_id}-stalled-1-*"))
    serve.serve_once(root)
    fakes.until(lambda: "done:2" in hub.store.load(task_id)["notified"], what="run 2 ended")
    launcher.join()
    assert "Answer to the earlier question: green" in json.loads((fake / "state.json").read_text())["prompts"][-1]
