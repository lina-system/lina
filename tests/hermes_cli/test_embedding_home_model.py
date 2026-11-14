"""lina#651: the embedding model is the home's; an agent, `lina store reembed` and `lina doctor` read the same one."""

from __future__ import annotations

import argparse
import json

import httpx
import pytest

from agent.embedding_client import EmbeddingClient, EmbeddingSettings
from plugins.memory import import_provider_module

HOME_MODEL = "hub/nous/qwen/qwen3-embedding-8b"


@pytest.fixture
def profile_home(tmp_path, monkeypatch):
    """A home whose root config.yaml sets the model and a profile ``coder`` whose own config.yaml sets none."""
    root = tmp_path / "home"
    profile = root / "profiles" / "coder"
    profile.mkdir(parents=True)
    (root / "config.yaml").write_text(
        f"embedding:\n  base_url: http://127.0.0.1:9/v1\n  model: {HOME_MODEL}\n", encoding="utf-8")
    (profile / "config.yaml").write_text("embedding:\n  base_url: http://127.0.0.1:9/v1\n", encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(profile))
    return profile


def _store_present(tmp_path, monkeypatch):
    from hermes_cli import store
    (tmp_path / "store.env").write_text("LINA_STORE_PASSWORD=placeholder\n", encoding="utf-8")
    monkeypatch.setattr(store, "store_dir", lambda: tmp_path)


def test_the_agent_reembed_and_doctor_use_the_home_model_when_the_profile_sets_none(
        profile_home, tmp_path, monkeypatch, capsys):
    sent = []

    def handler(request: httpx.Request) -> httpx.Response:
        sent.append(json.loads(request.content)["model"])
        return httpx.Response(200, json={"data": [{"index": 0, "embedding": [1.0] * 1024}]})

    with EmbeddingClient(EmbeddingSettings.from_config(), transport=httpx.MockTransport(handler)) as client:
        client.embed_one("what tea does the owner like")

    from hermes_cli import store_reembed
    reembedded = []
    monkeypatch.setattr(store_reembed, "reembed", lambda connect, client, *a, **k: reembedded.append(client.model))
    args = argparse.Namespace(from_index=None, source="memory", batch_size=None, from_start=False, max_batches=None,
                              max_rate=None)
    assert store_reembed.run(args) == 0

    from hermes_cli.doctor_state import _check_memory_vectors
    _store_present(tmp_path, monkeypatch)
    counted = []
    pending_vectors = import_provider_module("honcho", "pending_vectors")
    monkeypatch.setattr(pending_vectors, "pending_counts", lambda model, workspace=None: counted.append(model) or {})
    _check_memory_vectors(False)

    assert sent == reembedded == counted == [HOME_MODEL]
    assert f"all embedded with {HOME_MODEL}" in capsys.readouterr().out


def test_doctor_names_a_profile_that_sets_another_model(profile_home, capsys):
    from hermes_cli.doctor_state import _check_embedding_model
    (profile_home / "config.yaml").write_text("embedding:\n  model: qwen3-embedding-8b\n", encoding="utf-8")

    finding = _check_embedding_model(False)

    assert "Profile coder sets embedding.model 'qwen3-embedding-8b'" in capsys.readouterr().out
    assert [issue for issue in finding.manual_issues if "Profile 'coder'" in issue]
