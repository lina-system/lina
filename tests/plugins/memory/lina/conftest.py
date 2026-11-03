"""Fixtures of the lina-memory tests (lina#20); the home and agent helpers are in ``harness.py``."""

from __future__ import annotations

import sys

import pytest

from tests.plugins.memory.lina.harness import FakeHoncho


@pytest.fixture
def lina_env(monkeypatch):
    from hermes_cli.plugins import _reset_plugin_managers_for_tests

    monkeypatch.setenv("LCM_EMBEDDINGS_ENABLED", "false")
    monkeypatch.setitem(sys.modules, "tiktoken", None)
    fake = FakeHoncho()
    import plugins.memory.honcho.client as honcho_client
    import plugins.memory.honcho.session as honcho_session
    monkeypatch.setattr(honcho_client, "get_honcho_client", lambda *a, **k: fake)
    monkeypatch.setattr(honcho_session, "get_honcho_client", lambda *a, **k: fake)
    _reset_plugin_managers_for_tests()
    yield fake
    _reset_plugin_managers_for_tests()
