"""The Honcho settings a store install gives the api and deriver (lina#156).

Honcho refuses to start when its EMBEDDING_VECTOR_DIMENSIONS differs from the width of ``vec.embeddings``
(migration 0003). These tests read what the containers would get: the person's ``honcho.env`` (compose
``env_file``) with the ``x-honcho`` ``environment`` of compose.yaml over it. The width must be set there, since
Honcho's own default (1536) is not the width of ``vec.embeddings``.
"""

from __future__ import annotations

import re
from pathlib import Path

import pytest
from ruamel.yaml import YAML

from agent.embedding_client import DEFAULT_MODEL
from hermes_cli import store as st
from tests.hermes_cli.test_store import FakeDocker, make_store

MIGRATION_0003 = st.SERVICES_DIR / "store" / "migrations" / "0003_vec_embeddings.sql"
OLD_HONCHO_ENV = "# Honcho settings (LLM_*, EMBEDDING_*, AUTH_*), one KEY=value per line. See services/honcho.\n"


@pytest.fixture
def honcho_src(tmp_path, monkeypatch):
    src = tmp_path / "services" / "honcho"
    src.mkdir(parents=True)
    (src / "Dockerfile").write_text("FROM scratch\n")
    monkeypatch.setattr(st, "HONCHO_DIR", src)
    return src


def vec_embeddings_width() -> int:
    return int(re.search(r"embedding vector\((\d+)\)", MIGRATION_0003.read_text()).group(1))


def honcho_container_env(store: st.Store) -> dict[str, str]:
    honcho = YAML(typ="safe").load(st.COMPOSE_FILE.read_text())["x-honcho"]
    assert honcho["env_file"][0]["path"].startswith("${LINA_HONCHO_ENV")
    env = st.read_env(Path(store.values()["LINA_HONCHO_ENV"]))
    env.update({key: str(value) for key, value in honcho["environment"].items()})
    return env


def honcho_width(env: dict[str, str]) -> int:
    assert "EMBEDDING_VECTOR_DIMENSIONS" in env, "Honcho would run on its own default width"
    return int(env["EMBEDDING_VECTOR_DIMENSIONS"])


def test_a_fresh_install_gives_honcho_the_width_and_model_of_vec_embeddings(tmp_path, honcho_src):
    store = make_store(tmp_path, FakeDocker(), [])
    st.install(store)
    env = honcho_container_env(store)
    assert honcho_width(env) == vec_embeddings_width()
    assert env["EMBEDDING_MODEL_CONFIG__MODEL"] == DEFAULT_MODEL


def test_an_upgrade_of_an_install_from_before_lina_156_gets_the_width(tmp_path, honcho_src):
    store = make_store(tmp_path, FakeDocker(volume_exists=True), [])
    store.directory.mkdir()
    (store.directory / "honcho.env").write_text(OLD_HONCHO_ENV)
    st.install(store)
    assert honcho_width(honcho_container_env(store)) == vec_embeddings_width()


def test_what_honcho_env_already_sets_wins_over_the_defaults(tmp_path, honcho_src):
    store = make_store(tmp_path, FakeDocker(volume_exists=True), [])
    store.directory.mkdir()
    own = OLD_HONCHO_ENV + "LLM_OPENAI_API_KEY=own\nEMBEDDING_VECTOR_DIMENSIONS=1536"  # no newline at the end
    (store.directory / "honcho.env").write_text(own)
    st.install(store)
    env = honcho_container_env(store)
    assert honcho_width(env) == 1536
    assert env["LLM_OPENAI_API_KEY"] == "own"
    assert env["EMBEDDING_MODEL_CONFIG__MODEL"] == DEFAULT_MODEL
    assert (store.directory / "honcho.env").read_text().startswith(own + "\n")
