"""The OCX model list of the work settings (lina#256) on recorded ``models live`` / ``account list --quota`` shapes with
synthetic values: only enabled rows, efforts, state, quota left (100 - the used percent) and reset per model, the
per-runner check with its warning line, and the last good list while OCX cannot be read."""

from __future__ import annotations

import textwrap

import pytest

from hermes_cli import lina_models, lina_ocx

pytestmark = pytest.mark.platforms("posix")

T = 1_700_000_000
LIVE = [
    {"provider": "openai", "id": "m-open", "namespaced": "openai/m-open", "disabled": False,
     "reasoningEfforts": ["low", "medium", "high"], "defaultReasoningEffort": "medium", "displayName": "Open"},
    {"provider": "openai", "id": "m-off", "namespaced": "openai/m-off", "disabled": True,
     "reasoningEfforts": ["high"], "defaultReasoningEffort": "high"},
    {"provider": "openai", "id": "m-pending", "namespaced": "openai/m-pending", "disabled": False,
     "initialSelectionPending": True, "reasoningEfforts": [], "defaultReasoningEffort": None},
    {"provider": "anthropic", "id": "m-claude-a", "namespaced": "anthropic/m-claude-a", "disabled": False,
     "reasoningEfforts": ["low", "high"], "defaultReasoningEffort": "high"},
    {"provider": "anthropic", "id": "m-claude-b", "namespaced": "anthropic/m-claude-b", "disabled": False,
     "reasoningEfforts": ["high"], "defaultReasoningEffort": "high"},
    {"provider": "xai", "id": "m-grok", "namespaced": "xai/m-grok", "disabled": False},
    {"provider": "google", "id": "m-gem", "namespaced": "google/m-gem", "disabled": False},
    {"provider": "cursor", "id": "m-cur", "namespaced": "cursor/m-cur", "disabled": False},
    {"provider": "zai", "id": "m-out", "namespaced": "zai/m-out", "disabled": False},
]
QUOTA = {"accounts": [
    {"provider": "openai", "id": "a1", "paused": False, "needsReauth": False,
     "quota": {"weeklyPercent": 30, "weeklyResetAt": T + 1000, "fiveHourPercent": 80, "fiveHourResetAt": T + 500}},
    {"provider": "openai", "id": "a2", "paused": False, "needsReauth": False,
     "quota": {"weeklyPercent": 50, "weeklyResetAt": T + 2000}},
    {"provider": "openai", "id": "a3", "paused": True, "quota": {"weeklyPercent": 0, "weeklyResetAt": T + 10}},
    {"provider": "anthropic", "id": "b1", "quota": {
        "weeklyPercent": 10, "weeklyResetAt": (T + 3000) * 1000,
        "customWindows": [{"label": "one-model", "percent": 100, "resetAt": (T + 4000) * 1000, "modelId": "m-claude-b"}]}},
    {"provider": "anthropic", "id": "b2", "quota": {"weeklyPercent": 60, "weeklyResetAt": (T + 5000) * 1000}},
    {"provider": "xai", "id": "c1", "paused": True, "quota": {"weeklyPercent": 1, "weeklyResetAt": T + 10}},
    {"provider": "google", "id": "d1", "type": "api-key", "quota": None},
    {"provider": "cursor", "id": "e1", "needsReauth": True, "quota": None},
    {"provider": "zai", "id": "f1", "quota": {"weeklyPercent": 100, "weeklyResetAt": T + 6000}},
], "notes": []}
CONFIG = textwrap.dedent("""\
    devices:
      workers:
        here: {host: local}
    worker:
      runners:
        codex: {type: command, argv: [codex, "-"], model: "openai/m-off:high", fallback_model: anthropic/m-claude-a}
        dsh: {type: command, argv: [dsh, "-"], model: zai/m-out}
        agy: {type: command, argv: [agy, "-"], model: "openai/m-open:medium"}
      models:
        aliases:
          planner: {provider: xai, id: m-grok, thinking: high}
        defaults: {research: planner}
        fallback: openai/m-open
    """)


class FakeOcx:
    def __init__(self):
        self.error: str | None = None
        self.calls: list[tuple] = []

    def run(self, args, root=None):
        args = tuple(args)
        self.calls.append(args)
        assert "--refresh" not in args
        if self.error:
            raise lina_ocx.OcxError(self.error)
        return {("models", "live", "--json"): LIVE, ("account", "list", "--quota", "--json"): QUOTA}[args]


@pytest.fixture
def ocx(tmp_path, monkeypatch):
    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text(CONFIG, encoding="utf-8")
    monkeypatch.setenv("LINA_HOME", str(base))
    fake = FakeOcx()
    monkeypatch.setattr(lina_models, "run", fake.run)
    from hermes_cli import lina_work_settings_models
    lina_work_settings_models.forget()
    return fake


def _view(now=T):
    from hermes_cli.lina_work_settings_models import view
    return view(now=now)


def test_only_enabled_rows_are_listed_each_with_efforts_state_left_and_reset(ocx):
    models = {(m["provider"], m["id"]): m for m in _view()["models"]}
    assert ("openai", "m-off") not in models and ("openai", "m-pending") not in models
    open_ = models[("openai", "m-open")]
    assert open_["efforts"] == ["low", "medium", "high"] and open_["defaultEffort"] == "medium"
    assert (open_["state"], open_["left"], open_["resetAt"], open_["accounts"]) == ("ok", 50, T + 2000, 2)
    assert {key: (m["state"], m["left"]) for key, m in models.items() if key[0] != "openai"} == {
        ("anthropic", "m-claude-a"): ("ok", 90),
        ("anthropic", "m-claude-b"): ("ok", 40),  # the window bound to m-claude-b leaves b1 out for it only
        ("xai", "m-grok"): ("no_account", None),  # its one account is paused
        ("google", "m-gem"): ("unknown", None),  # an API key with no quota data
        ("cursor", "m-cur"): ("no_account", None),  # waiting to sign in again
        ("zai", "m-out"): ("out_of_quota", 0),
    }
    assert models[("anthropic", "m-claude-a")]["resetAt"] == T + 3000  # milliseconds read as seconds
    assert models[("anthropic", "m-claude-b")]["resetAt"] == T + 5000
    assert models[("zai", "m-out")]["resetAt"] == T + 6000
    assert set(ocx.calls) == {("models", "live", "--json"), ("account", "list", "--quota", "--json")}


def test_each_runner_mode_default_and_the_fallback_say_whether_their_model_is_usable(ocx):
    data = _view()
    runners = {row["name"]: row for row in data["runners"]}
    assert (runners["codex"]["state"], runners["codex"]["usable"]) == ("off", False)
    assert runners["codex"]["warning"] == "codex 모델이 꺼져 있습니다 · 다음 실행은 anthropic/m-claude-a로 갑니다"
    assert runners["dsh"]["state"] == "out_of_quota" and runners["dsh"]["warning"].startswith("dsh 모델")
    assert "쿼터" in runners["dsh"]["warning"] and runners["dsh"]["warning"].endswith("openai/m-open로 갑니다")
    assert runners["agy"]["usable"] is True and runners["agy"]["warning"] is None
    assert runners["omo"]["model"] is None and runners["omo"]["warning"] is None
    modes = {row["mode"]: row for row in data["modes"]}
    assert modes["research"]["state"] == "no_account" and modes["research"]["warning"]
    assert data["fallback"]["model"] == "openai/m-open" and data["fallback"]["usable"] is True
    assert data["warnings"] == [w for w in (runners["codex"]["warning"], runners["dsh"]["warning"],
                                            modes["research"]["warning"]) if w]
    assert data["errors"] == []


@pytest.mark.parametrize("models", ["x", 3, ["planner"], {"defaults": ["research"], "aliases": "y", "fallback": 4}])
def test_a_wrong_typed_models_section_written_by_hand_reads_as_absent(ocx, tmp_path, models):
    import json as _json
    path = tmp_path / "lina" / "config.yaml"
    path.write_text(CONFIG.split("\n  models:")[0] + f"\n  models: {_json.dumps(models)}\n", encoding="utf-8")
    data = _view()
    assert data["modes"] == [] and {row["name"] for row in data["runners"]} >= {"omo", "codex"}


def test_ocx_unreadable_serves_the_last_good_list_for_ten_minutes_with_an_error_and_writes_nothing(ocx, tmp_path):
    good = _view(T)
    config = (tmp_path / "lina" / "config.yaml").read_bytes()
    ocx.error = "the OCX of ocx.external is not running"
    stale = _view(T + 300)
    assert stale["models"] == good["models"]
    assert stale["errors"] == [{"source": "ocx", "message": "the OCX of ocx.external is not running"}]
    gone = _view(T + 601)
    assert gone["models"] == [] and gone["errors"][0]["source"] == "ocx"
    assert {row["state"] for row in gone["runners"] if row["model"]} == {"unknown"}
    assert (tmp_path / "lina" / "config.yaml").read_bytes() == config
