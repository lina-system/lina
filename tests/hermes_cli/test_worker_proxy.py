"""Node-local magpie routing preserves written model names and refuses missing providers (lina#585)."""

from __future__ import annotations

import pytest

from hermes_cli.worker import models, proxy
from hermes_cli.worker.runners.base import ModelProfile, RunnerError, Task
from hermes_cli.worker.runners.omo import OmoRunner

pytestmark = pytest.mark.platforms("posix")

WORKER = {"models": {"aliases": {
    "opus": {"provider": "claude", "id": "m-opus", "thinking": "high"},
    "astra": {"provider": "codex", "id": "m-astra", "thinking": "xhigh"},
}}}
MAGPIE = proxy.Proxy()
NAMES = {"opus": "claude/m-opus", "astra": "codex/m-astra",
         "vendor/a/b": "vendor/a/b", "codex/m-fast": "codex/m-fast"}


@pytest.mark.parametrize("written", NAMES)
def test_every_alias_keeps_its_written_identity_and_routes_through_magpie(written):
    profile = models.parse(written, WORKER)
    assert models.name(profile).split(":")[0] == NAMES[written]
    assert models.through_proxy(profile, MAGPIE) == ("magpie", "hub/" + NAMES[written])


def test_a_model_with_no_provider_is_refused():
    with pytest.raises(RunnerError, match="write it as <provider>/<id>"):
        models.through_proxy(ModelProfile(None, "m-out", None), MAGPIE)


def test_absent_config_defaults_to_magpie_and_unused_keys_are_ignored():
    assert proxy.of({}) == proxy.of({"proxy": {}}) == proxy.Proxy()
    assert proxy.of({"proxy": {"kind": "unused", "read": ["unused"]}}) == proxy.Proxy()
    assert proxy.of({"proxy": {"remote_id": "node-a", "health": "http://127.0.0.1:1/v1/models"}}) == \
        proxy.Proxy("node-a", "http://127.0.0.1:1/v1/models")


def test_an_absent_remote_id_is_hub_and_an_empty_one_is_kept():
    assert proxy.of({"proxy": {"kind": "magpie"}}).remote_id == "hub"
    assert proxy.of({"proxy": {"kind": "magpie", "remote_id": ""}}).remote_id == ""


@pytest.mark.parametrize("remote_id", ["a/b", "/", 1, ["hub"]])
def test_a_remote_id_with_a_slash_or_not_a_text_is_refused(remote_id):
    with pytest.raises(RunnerError, match="worker.proxy.remote_id"):
        proxy.of({"proxy": {"kind": "magpie", "remote_id": remote_id}})


@pytest.mark.parametrize("written", NAMES)
def test_a_node_whose_own_magpie_holds_the_models_opens_the_plain_name_with_the_same_identity(written):
    identity = NAMES[written]
    assert models.through_proxy(models.parse(written, WORKER), proxy.Proxy("")) == ("magpie", identity)


def _runner(root):
    return OmoRunner("omo", {"bin": str(root / "omo"), "node": str(root / "node"), "engine": str(root / "engine")},
                     root=root)


@pytest.mark.parametrize(("config", "session"), [
    (None, ["magpie", "hub/claude/m-opus", "high"]),
    ("worker:\n  proxy: {}\n", ["magpie", "hub/claude/m-opus", "high"]),
    ('worker:\n  proxy: {remote_id: ""}\n', ["magpie", "claude/m-opus", "high"]),
])
def test_the_omo_session_opens_through_this_nodes_proxy(tmp_path, monkeypatch, config, session):
    if config:
        (tmp_path / "config.yaml").write_text(config, encoding="utf-8")
    runner = _runner(tmp_path)
    calls: list[list[str]] = []
    monkeypatch.setattr(runner, "_call", lambda argv, timeout, stderr=None: (calls.append(argv), ({}, 0))[1])

    runner._open_session(Task("T-1", tmp_path / "task"), tmp_path / "prompt.1", models.parse("opus", WORKER), 1,
                         "workspace-write")

    assert calls[0][6:9] == session
