"""The model proxy per node (lina#585): ``worker.proxy`` in a node's own config says ``ocx`` (absent) or ``magpie``,
a model has one identity in magpie's catalog form whichever form it was written in, and the omo runner opens its
session through the node's proxy. Aliases and ids are synthetic; omo, OCX and magpie are never run."""

from __future__ import annotations

import pytest

from hermes_cli.worker import models, proxy, record
from hermes_cli.worker.runners.base import ModelProfile, RunnerError, Task
from hermes_cli.worker.runners.omo import OmoRunner

pytestmark = pytest.mark.platforms("posix")

WORKER = {"models": {"aliases": {
    "opus": {"provider": "opencodex", "id": "anthropic/claude-opus-5-5", "thinking": "high"},
    "astra": {"provider": "opencodex", "id": "gpt-6-astra", "thinking": "xhigh"},
    "fable": {"provider": "opencodex", "id": "anthropic/claude-fable-5", "thinking": "medium"},
}}}
OCX = proxy.Proxy()
MAGPIE = proxy.Proxy("magpie", "hub")

# written form -> (identity, session on an ocx node, session on a magpie node)
NAMES = {
    "opus": ("claude/claude-opus-5-5", ("opencodex", "anthropic/claude-opus-5-5"),
             ("magpie", "hub/claude/claude-opus-5-5")),
    "astra": ("codex/gpt-6-astra", ("opencodex", "gpt-6-astra"), ("magpie", "hub/codex/gpt-6-astra")),
    "fable": ("claude/claude-fable-5", ("opencodex", "anthropic/claude-fable-5"), ("magpie", "hub/claude/claude-fable-5")),
    "opencodex/gpt-5.5": ("codex/gpt-5.5", ("opencodex", "gpt-5.5"), ("magpie", "hub/codex/gpt-5.5")),
    "google/gemini-3-pro": ("antigravity/gemini-3-pro", ("opencodex", "google/gemini-3-pro"),
                            ("magpie", "hub/antigravity/gemini-3-pro")),
    "opencodex/google-antigravity/gemini-3-flash": (
        "antigravity/gemini-3-flash", ("opencodex", "google-antigravity/gemini-3-flash"),
        ("magpie", "hub/antigravity/gemini-3-flash")),
    "xai/grok-4.6": ("grok/grok-4.6", ("opencodex", "xai/grok-4.6"), ("magpie", "hub/grok/grok-4.6")),
}


@pytest.mark.parametrize("written", NAMES)
def test_every_alias_has_one_identity_and_a_session_on_both_kinds(written):
    identity, on_ocx, on_magpie = NAMES[written]
    profile = models.parse(written, WORKER)

    assert models.name(profile).split(":")[0] == identity
    assert models.through_proxy(profile, OCX) == on_ocx
    assert models.through_proxy(profile, MAGPIE) == on_magpie


@pytest.mark.parametrize(("magpie_form", "ocx_session"), [
    ("claude/claude-opus-5-5", ("opencodex", "anthropic/claude-opus-5-5")),
    ("codex/gpt-5.5", ("opencodex", "gpt-5.5")),
    ("grok/grok-4.6", ("opencodex", "xai/grok-4.6")),
    ("hub/claude/claude-opus-5-5", ("opencodex", "anthropic/claude-opus-5-5")),
    ("magpie/hub/codex/gpt-5.5", ("opencodex", "gpt-5.5")),
])
def test_the_table_reads_back_from_magpie_form_to_ocx_form(magpie_form, ocx_session):
    profile = models.parse(magpie_form, {})

    assert models.through_proxy(profile, OCX) == ocx_session
    assert models.through_proxy(profile, proxy.Proxy("magpie", "node-a"))[1].startswith("node-a/")
    assert "/".join(models.canonical(profile.provider, profile.id)) == "/".join(
        models.canonical(*models.through_proxy(profile, OCX)))


def test_an_ambiguous_reverse_is_refused_naming_both_and_the_ocx_form_still_runs():
    with pytest.raises(RunnerError, match="'google' or 'google-antigravity'"):
        models.through_proxy(models.parse("antigravity/gemini-3-pro", {}), OCX)

    assert models.through_proxy(models.parse("google-antigravity/gemini-3-pro", {}), OCX) == (
        "opencodex", "google-antigravity/gemini-3-pro")


def test_an_unknown_model_keeps_its_name_through_ocx_and_is_refused_by_magpie():
    profile = models.parse("zai/m-out", {})

    assert models.canonical(profile.provider, profile.id) == ("zai", "m-out")
    assert models.through_proxy(profile, OCX) == ("opencodex", "zai/m-out")
    with pytest.raises(RunnerError, match="zai/m-out"):
        models.through_proxy(profile, MAGPIE)


@pytest.mark.parametrize("fast", ["opencodex/gpt-5.5-fast", "opencodex/gpt-5.5--fast",
                                  "anthropic/claude-opus-5-5--fast"])
def test_a_fast_variant_runs_through_ocx_and_is_refused_by_magpie_with_its_name(fast):
    profile = models.parse(fast, {})

    assert models.through_proxy(profile, OCX)[0] == "opencodex"
    with pytest.raises(RunnerError, match=f"model {fast}: a --fast variant"):
        models.through_proxy(profile, MAGPIE)


def test_absent_config_is_ocx_and_a_wrong_kind_is_refused_by_its_key():
    assert proxy.of({}) == proxy.of({"proxy": {}}) == proxy.Proxy("ocx", "hub", None)
    assert proxy.of({"proxy": {"kind": "magpie", "remote_id": "node-a", "health": "http://127.0.0.1:1/healthz"}}) == \
        proxy.Proxy("magpie", "node-a", "http://127.0.0.1:1/healthz")
    with pytest.raises(RunnerError, match="worker.proxy.kind"):
        proxy.of({"proxy": {"kind": "openrouter"}})


def test_an_older_record_in_ocx_names_is_read_through_the_table():
    job = {"model": {"provider": "opencodex", "id": "anthropic/claude-opus-5-5", "thinking": "high"}}

    assert record.served_model(job)["provider"] == "claude"
    assert models.label(job["model"]) == "claude/claude-opus-5-5:high"


def _runner(root):
    return OmoRunner("omo", {"bin": str(root / "omo"), "node": str(root / "node"), "engine": str(root / "engine")},
                     root=root)


@pytest.mark.parametrize(("config", "session"), [
    (None, ["opencodex", "anthropic/claude-opus-5-5", "high"]),
    ("worker:\n  proxy: {kind: magpie}\n", ["magpie", "hub/claude/claude-opus-5-5", "high"]),
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


def test_a_magpie_node_opens_no_session_for_a_fast_variant(tmp_path, monkeypatch):
    (tmp_path / "config.yaml").write_text("worker:\n  proxy: {kind: magpie}\n", encoding="utf-8")
    runner = _runner(tmp_path)
    calls: list[list[str]] = []
    monkeypatch.setattr(runner, "_call", lambda argv, timeout, stderr=None: (calls.append(argv), ({}, 0))[1])

    with pytest.raises(RunnerError, match="gpt-5.5-fast"):
        runner._open_session(Task("T-1", tmp_path / "task"), tmp_path / "prompt.1",
                             ModelProfile("opencodex", "gpt-5.5-fast", None), 1, "workspace-write")
    assert calls == []
