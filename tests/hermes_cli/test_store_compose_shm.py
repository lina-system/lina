"""The store database gets enough shared memory for parallel queries over a populated store (lina#185)."""

from __future__ import annotations

from ruamel.yaml import YAML

from hermes_cli import store as st


def test_db_shm_size_is_above_the_docker_default():
    db = YAML(typ="safe").load(st.COMPOSE_FILE.read_text())["services"]["db"]
    assert db["shm_size"] == "1gb"
