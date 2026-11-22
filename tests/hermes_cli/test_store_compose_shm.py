"""The store database gets enough shared memory for parallel queries over a populated store (lina#185)."""

from __future__ import annotations

import re

from ruamel.yaml import YAML

from hermes_cli import store as st

DOCKER_DEFAULT_SHM = 64 * 1024 * 1024
MIN_SHM = 512 * 1024 * 1024
UNITS = {"": 1, "b": 1, "k": 1024, "kb": 1024, "m": 1024**2, "mb": 1024**2, "g": 1024**3, "gb": 1024**3}


def shm_bytes(value) -> int:
    """A compose ``shm_size``: a byte count or a size string such as ``1gb``, ``512m`` or ``1.5G``."""
    match = re.fullmatch(r"(\d+(?:\.\d+)?)\s*([a-z]*)", str(value).strip().lower())
    assert match and match.group(2) in UNITS, f"unreadable shm_size {value!r}"
    return int(float(match.group(1)) * UNITS[match.group(2)])


def test_shm_size_reader_takes_the_spellings_compose_accepts():
    assert shm_bytes("1gb") == shm_bytes("1G") == shm_bytes(1024**3) == shm_bytes("1024m") == 1024**3
    assert shm_bytes("512m") == MIN_SHM


def test_db_shm_size_is_above_the_docker_default():
    db = YAML(typ="safe").load(st.COMPOSE_FILE.read_text())["services"]["db"]
    assert shm_bytes(db["shm_size"]) >= MIN_SHM > DOCKER_DEFAULT_SHM
