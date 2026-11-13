"""Written model names and configured aliases agree across settings, runs and magpie sessions (lina#385)."""

from __future__ import annotations

import time
from pathlib import Path

import pytest

from hermes_cli import lina_work_settings_models as wm
from hermes_cli.worker import models, record
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.runners.base import ModelProfile, Task
from hermes_cli.worker.runners.omo import OmoRunner
from tests.hermes_cli import _worker_fakes as fakes

pytestmark = pytest.mark.platforms("posix")

LIVE = [
    {"provider": "anthropic", "id": "m-a", "namespaced": "anthropic/m-a", "disabled": False},
    {"provider": "anthropic", "id": "m-off", "namespaced": "anthropic/m-off", "disabled": True},
    {"provider": "zai", "id": "m-out", "namespaced": "zai/m-out", "disabled": False},
]
QUOTA = {"accounts": [
    {"provider": "anthropic", "id": "a1", "quota": {"weeklyPercent": 40}},
    {"provider": "zai", "id": "z1", "quota": {"weeklyPercent": 100}},
]}
LIST_FORM = ("anthropic/m-a:high", "anthropic/m-off", "zai/m-out")
ALIAS_FORM = ("active", "unlisted", "out")
CONFIG = """\
worker:
  runners:
    l-ok: {type: command, argv: [x, "-"], model: "anthropic/m-a:high", access: workspace-write}
    o-ok: {type: command, argv: [x, "-"], model: "active", access: workspace-write}
    l-off: {type: command, argv: [x, "-"], model: anthropic/m-off, access: workspace-write}
    o-off: {type: command, argv: [x, "-"], model: unlisted, access: workspace-write}
    l-out: {type: command, argv: [x, "-"], model: zai/m-out, access: workspace-write}
    o-out: {type: command, argv: [x, "-"], model: out, access: workspace-write}
    alias: {type: command, argv: [x, "-"], model: magpie-a, access: workspace-write}
  models:
    aliases:
      active: {provider: anthropic, id: m-a, thinking: high}
      unlisted: {provider: anthropic, id: m-off}
      out: {provider: zai, id: m-out}
      magpie-a: {provider: anthropic, id: m-a, thinking: high}
    fallback: anthropic/m-a
"""


@pytest.fixture
def base(tmp_path, monkeypatch):
    home = tmp_path / "lina"
    home.mkdir()
    (home / "config.yaml").write_text(CONFIG, encoding="utf-8")
    monkeypatch.setenv("LINA_HOME", str(home))
    from hermes_cli import lina_magpie
    monkeypatch.setattr(lina_magpie, "get", lambda proxy, path: {"data": [
        {"id": f"hub/{r['provider']}/{r['id']}"} for r in LIVE if not r.get("disabled")]})
    monkeypatch.setattr(lina_magpie, "model_accounts", lambda proxy: QUOTA["accounts"])
    wm.forget()
    wm.read(time.time())  # the models view has just read magpie: decide checks against this last good list
    yield home
    wm.forget()


def _job(home: Path, tmp_path: Path, **opts) -> dict:
    hub = Hub(home / "worker", out=lambda *_: None)
    brief = Path(fakes.brief(tmp_path, "do it"))
    return hub.make_job(brief, {"bot": "sion", "topic": "t", "mode": "ulw", "inputs": [], **opts}, None)


def _fallback(home: Path, job: dict) -> dict:
    receipts = Hub(home / "worker", out=lambda *_: None).store.receipts_of(job["id"])
    return next(r for r in receipts if r["kind"] == "model_fallback")


def test_both_forms_get_the_same_state_from_the_one_magpie_row(base):
    runners = {row["name"]: row for row in wm.view()["runners"]}

    for kind, state in (("ok", "ok"), ("off", "off"), ("out", "out_of_quota")):
        listed, through = runners[f"l-{kind}"], runners[f"o-{kind}"]
        assert (listed["state"], through["state"]) == (state, state)
        assert listed["usable"] == through["usable"]
    assert runners["alias"]["state"] == "ok" and runners["alias"]["warning"] is None
    assert wm.view()["fallback"]["state"] == "ok"


def test_decide_picks_the_same_model_and_the_same_fallback_for_both_forms(base, tmp_path):
    for listed, through in zip(LIST_FORM, ALIAS_FORM):
        one, other = _job(base, tmp_path, runner="alias", model=listed), _job(base, tmp_path, runner="alias",
                                                                               model=through)
        assert record.served_model(one) == record.served_model(other)
        assert one["settings"]["model_state"] == other["settings"]["model_state"] != "unchecked"
    for kind, named in (("off", "anthropic/m-off"), ("out", "zai/m-out")):
        one, other = _job(base, tmp_path, runner=f"l-{kind}"), _job(base, tmp_path, runner=f"o-{kind}")
        assert one["model"]["source"].startswith("fallback:")
        assert (one["model"], one["settings"]) == (other["model"], other["settings"])
        assert [r["from"] for r in (_fallback(base, one), _fallback(base, other))] == [named, named]


def test_the_run_record_shows_one_form_whichever_was_written(base, tmp_path):
    served = [record.served_model(_job(base, tmp_path, runner="alias", model=value))
              for value in ("anthropic/m-a:high", "active", "magpie-a")]

    assert [(s["provider"], s["id"], s["thinking"]) for s in served] == [("anthropic", "m-a", "high")] * 3
    labels = [models.label(_job(base, tmp_path, runner="alias", model=v)["model"])
              for v in ("anthropic/m-off:low", "zai/m-out:low", "magpie-a")]
    assert labels == ["anthropic/m-off:low", "zai/m-out:low", "magpie-a"]
    assert models.label({"provider": "anthropic", "id": "m-a", "alias": "magpie-a"}) == "magpie-a"


@pytest.mark.parametrize(("model", "provider", "model_id"), [
    (ModelProfile("anthropic", "m-a", "medium"), "magpie", "hub/anthropic/m-a"),
    (ModelProfile("zai", "m-out", "medium"), "magpie", "hub/zai/m-out"),
    (ModelProfile("anthropic", "m-a", "medium", "magpie-a", "mode:ulw"), "magpie", "hub/anthropic/m-a"),
])
def test_the_omo_session_opens_through_magpie_with_the_namespaced_id(tmp_path, monkeypatch, model, provider, model_id):
    runner = OmoRunner("omo", {"bin": str(tmp_path / "omo"), "node": str(tmp_path / "node"),
                               "engine": str(tmp_path / "engine")}, root=tmp_path)
    calls: list[list[str]] = []
    monkeypatch.setattr(runner, "_call", lambda argv, timeout, stderr=None: (calls.append(argv), ({}, 0))[1])
    task = Task("T-1", tmp_path / "task")

    runner._open_session(task, tmp_path / "prompt.1", model, 1, "workspace-write")

    assert calls[0][1].endswith("omo_open.mjs")
    assert calls[0][6:9] == [provider, model_id, "medium"]
