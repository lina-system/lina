"""The run path reads the work settings at submit (lina#257): the runner from ``--runner``, the routing rules or the
default; the model from ``--model``, the runner's model or the mode default; the fallback model when OCX has the
chosen one off or out of quota; the revision, both sources and the OCX check on the job and its ``submitted``
receipt; and on the device the job's model over the device's own runner entry. OCX is a fake with synthetic rows."""

from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest

from hermes_cli import lina_models, lina_ocx
from hermes_cli.worker import device
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.runners.base import ModelProfile, Task
from tests.hermes_cli import _worker_fakes as fakes

pytestmark = pytest.mark.platforms("posix")

LIVE = [
    {"provider": "prov", "id": "m-open", "disabled": False},
    {"provider": "prov", "id": "m-plan", "disabled": False},
    {"provider": "prov", "id": "m-off", "disabled": True},
    {"provider": "zed", "id": "m-out", "disabled": False},
    {"provider": "gone", "id": "m-gone", "disabled": False},
]
QUOTA = {"accounts": [
    {"provider": "prov", "id": "a1", "quota": {"weeklyPercent": 40}},
    {"provider": "zed", "id": "b1", "quota": {"weeklyPercent": 100}},
    {"provider": "gone", "id": "c1", "needsReauth": True, "quota": None},
]}
CONFIG = """\
worker:
  runners:
    codex: {type: command, argv: [codex, "-"], model_args: [--model, "{id}"], model: "prov/m-off:high",
            fallback_model: prov/m-open}
    dsh: {type: command, argv: [dsh, "-"], model: zed/m-out}
    fine: {type: command, argv: [fine, "-"], model: "prov/m-open:high"}
    stuck: {type: command, argv: [stuck, "-"], model: prov/m-off, fallback_model: gone/m-gone}
    agy: {type: command, argv: [agy, "-"]}
    idle: {type: command, argv: [idle, "-"], "on": false}
  models:
    aliases: {planner: {provider: prov, id: m-plan, thinking: medium}}
    defaults: {research: planner}
    fallback: prov/m-open
  routing:
    - {name: small, when: {runner_label: runner/dsh, size: [S]}, runner: dsh}
"""


class FakeOcx:
    def __init__(self):
        self.error: str | None = None

    def run(self, args, root=None):
        if self.error:
            raise lina_ocx.OcxError(self.error)
        return {("models", "live", "--json"): LIVE, ("account", "list", "--quota", "--json"): QUOTA}[tuple(args)]


@pytest.fixture
def ocx(monkeypatch):
    fake = FakeOcx()
    monkeypatch.setattr(lina_models, "run", fake.run)
    from hermes_cli import lina_work_settings_models
    lina_work_settings_models.forget()
    yield fake
    lina_work_settings_models.forget()


@pytest.fixture
def hub(tmp_path, ocx):
    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text(CONFIG, encoding="utf-8")
    return Hub(base / "worker", out=lambda *_: None)


def _job(hub: Hub, tmp_path: Path, **opts) -> dict:
    brief = Path(fakes.brief(tmp_path, "do it"))
    return hub.make_job(brief, {"bot": "sion", "topic": "t", "mode": "ulw", "inputs": [], **opts}, None)


def _picked(job: dict) -> tuple:
    model = job["model"] or {}
    return job["runner"], (model.get("provider"), model.get("id"), model.get("thinking")), job["settings"]


def test_the_runner_is_the_flag_else_the_routing_rule_else_the_default_and_an_off_runner_is_refused(hub, tmp_path):
    routed = _job(hub, tmp_path, labels=["enhancement", "runner/dsh"], size="S")
    other_size = _job(hub, tmp_path, labels=["runner/dsh"], size="L")
    flagged = _job(hub, tmp_path, runner="agy", labels=["runner/dsh"], size="S")

    assert (routed["runner"], routed["settings"]["runner_source"]) == ("dsh", "routing:small")
    assert (other_size["runner"], other_size["settings"]["runner_source"]) == ("omo", "default")
    assert (flagged["runner"], flagged["settings"]["runner_source"]) == ("agy", "--runner")
    assert routed["settings"]["rev"] == "r1"  # the config's settings, recorded as the first revision on read
    with pytest.raises(HubError, match=r"^worker\.runners\.idle\.on: runner 'idle' is off$"):
        _job(hub, tmp_path, runner="idle")


def test_the_model_is_the_flag_else_the_runner_s_else_the_mode_default_and_the_receipt_carries_it(hub, tmp_path):
    flag = _job(hub, tmp_path, runner="fine", model="zed/m-out")
    own = _job(hub, tmp_path, runner="fine")
    mode = _job(hub, tmp_path, runner="agy", mode="research")
    none = _job(hub, tmp_path, runner="agy")

    assert _picked(flag)[1:] == (("zed", "m-out", None), {"rev": "r1", "runner_source": "--runner",
                                                          "model_source": "--model", "ocx_check": "out_of_quota"})
    assert _picked(own)[1:] == (("prov", "m-open", "high"), {"rev": "r1", "runner_source": "--runner",
                                                             "model_source": "runner:fine", "ocx_check": "ok"})
    assert _picked(mode)[1:] == (("prov", "m-plan", "medium"), {"rev": "r1", "runner_source": "--runner",
                                                                "model_source": "mode:research", "ocx_check": "ok"})
    assert none["model"] is None and none["settings"]["model_source"] == "runner"
    assert none["settings"]["ocx_check"] == "unchecked"
    submitted = hub.store.receipts_of(flag["id"])
    assert [r["kind"] for r in submitted] == ["submitted"]  # an explicit --model is never replaced
    assert submitted[0]["settings"] == flag["settings"] and submitted[0]["model"] == flag["model"]


def test_a_model_ocx_has_off_runs_on_the_runner_s_fallback_with_one_receipt(hub, tmp_path):
    job = _job(hub, tmp_path, runner="codex")

    assert _picked(job) == ("codex", ("prov", "m-open", None), {"rev": "r1", "runner_source": "--runner",
                                                                "model_source": "fallback:off", "ocx_check": "off"})
    receipts = hub.store.receipts_of(job["id"])
    assert [r["kind"] for r in receipts] == ["submitted", "model_fallback"]
    assert {k: receipts[1][k] for k in ("from", "to", "why")} == {"from": "prov/m-off:high", "to": "prov/m-open",
                                                                  "why": "off"}


def test_a_model_out_of_quota_runs_on_the_settings_fallback_and_an_unusable_fallback_keeps_the_model(hub, tmp_path):
    quota = _job(hub, tmp_path, runner="dsh")
    stuck = _job(hub, tmp_path, runner="stuck")

    assert _picked(quota)[1] == ("prov", "m-open", None)
    assert quota["settings"]["model_source"] == "fallback:out_of_quota"
    assert quota["settings"]["ocx_check"] == "out_of_quota"
    assert _picked(stuck)[1] == ("prov", "m-off", None)  # its fallback has no account: the run goes on as chosen
    assert (stuck["settings"]["model_source"], stuck["settings"]["ocx_check"]) == ("runner:stuck", "off")
    assert [r["kind"] for r in hub.store.receipts_of(stuck["id"])] == ["submitted"]


def test_ocx_unreadable_keeps_the_chosen_model_unchecked(hub, tmp_path, ocx):
    ocx.error = "OCX is not running"

    job = _job(hub, tmp_path, runner="codex")

    assert _picked(job)[1] == ("prov", "m-off", "high")
    assert (job["settings"]["model_source"], job["settings"]["ocx_check"]) == ("runner:codex", "unchecked")
    assert [r["kind"] for r in hub.store.receipts_of(job["id"])] == ["submitted"]


def test_on_the_device_the_job_s_model_wins_and_the_device_s_entry_gives_the_local_argv(tmp_path):
    base = tmp_path / "device"
    script = "import json, sys; open('argv.json', 'w').write(json.dumps(sys.argv[1:]))"
    fakes.write_config(base, f"codex: {{type: command, argv: {json.dumps([sys.executable, '-c', script])}, "
                             "model: other/m-device}\n")
    root = base / "worker"
    task = Task("OT-DEV0001", root / "tasks" / "OT-DEV0001", {})
    task.work.mkdir(parents=True)
    job = {"runner": "codex", "runner_spec": {"type": "command", "argv": ["codex", "-"],
                                              "model_args": ["--model", "{provider}/{id}"]},
           "model": {"provider": "prov", "id": "m-open", "thinking": "high", "alias": None, "source": "runner:codex"}}

    runner = device.runner_for(root, task, job)
    runner.start(task, "do it", ModelProfile.from_dict(job["model"]))

    argv_file = task.work / "argv.json"
    fakes.until(argv_file.is_file, what="the device's CLI ran")
    fakes.until(lambda: task.path("exit.1.1").is_file(), what="the device's CLI exited")
    assert json.loads(argv_file.read_text(encoding="utf-8")) == ["--model", "prov/m-open"]
