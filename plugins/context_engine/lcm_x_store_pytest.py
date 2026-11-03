"""pytest plugin: run LCM-X's own suite on the memory store (lina#16).

``pytest -p plugins.context_engine.lcm_x_store_pytest`` from inside ``plugins/context_engine/lcm-x`` (LINA root on
``PYTHONPATH``, as ``.github/workflows/lcm-x.yml`` runs it). Every database path a test opens goes to the test
Postgres of ``tests/_fixtures/memory_store.py`` (``LINA_TEST_PG_DSN``, a local ``initdb`` with pgvector, or
Docker), one agent per path, and every test starts with empty tables. Without a Postgres the run fails: this
plugin exists to run the suite there.
"""

from __future__ import annotations

import hashlib
import importlib.util
import os
import sys
from pathlib import Path

import pytest

_REPO = Path(__file__).resolve().parents[2]


def _memory_store_fixture():
    # LCM-X's own tests package is also called ``tests``, so LINA's fixture module loads by path.
    name = "lina_memory_store_fixture"
    if name not in sys.modules:
        spec = importlib.util.spec_from_file_location(name, _REPO / "tests" / "_fixtures" / "memory_store.py")
        module = importlib.util.module_from_spec(spec)
        sys.modules[name] = module
        spec.loader.exec_module(module)
    return sys.modules[name]


def _agent_for(path: str) -> str:
    return "lcm-test-" + hashlib.sha256(path.encode("utf-8", "surrogatepass")).hexdigest()[:16]


@pytest.fixture(autouse=True)
def _lcm_x_on_store(monkeypatch):
    import hermes_state_store
    from plugins.context_engine import lcm_x_store
    store = _memory_store_fixture().PROCESS_STORE
    dsn = store.app_dsn()
    monkeypatch.setenv("LINA_STORE_DSN", dsn)
    monkeypatch.setattr(hermes_state_store, "resolve_dsn", lambda: dsn)
    lcm_x_store.route_every_database(_agent_for)
    yield
    lcm_x_store.route_every_database(None)
    hermes_state_store.TEXT_CACHE.clear()
    store.reset()


def pytest_unconfigure(config) -> None:
    if "lina_memory_store_fixture" in sys.modules:
        sys.modules["lina_memory_store_fixture"].PROCESS_STORE.close()
    os.environ.pop("LINA_STORE_DSN", None)
