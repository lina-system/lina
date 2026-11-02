"""Memory and browser plugin credentials honor scoped wins and never borrow another profile key."""

from __future__ import annotations

import pytest

from agent.secret_scope import (
    reset_secret_scope,
    set_multiplex_active,
    set_secret_scope,
)


@pytest.fixture
def multiplex_scope():
    """Install a secret scope with multiplexing ON; restore state after."""

    def _install(scope: dict[str, str]):
        set_multiplex_active(True)
        token = set_secret_scope(scope)
        return token

    tokens = []

    def install(scope: dict[str, str]):
        tokens.append(_install(scope))

    yield install

    for token in tokens:
        reset_secret_scope(token)
    set_multiplex_active(False)


# ---------------------------------------------------------------------------
# Family A — memory plugins
# ---------------------------------------------------------------------------

class TestMemoryFamily:
    def test_honcho_scoped_key_wins(self, multiplex_scope, monkeypatch):
        monkeypatch.setenv("HONCHO_API_KEY", "env-other-profile")
        multiplex_scope({"HONCHO_API_KEY": "scoped-key"})

        from plugins.memory.honcho import HonchoMemoryProvider

        assert HonchoMemoryProvider().is_available() is True

    def test_honcho_scoped_miss_does_not_borrow_environ(
        self, multiplex_scope, monkeypatch
    ):
        # Env holds another profile's key; the active profile's scope has none.
        monkeypatch.setenv("HONCHO_API_KEY", "env-other-profile")
        multiplex_scope({})

        from plugins.memory.honcho import HonchoMemoryProvider

        assert HonchoMemoryProvider().is_available() is False




# ---------------------------------------------------------------------------
# Family C — browser/web plugins
# ---------------------------------------------------------------------------

class TestBrowserFamily:
    def test_firecrawl_scoped_key_wins(self, multiplex_scope, monkeypatch):
        monkeypatch.delenv("FIRECRAWL_API_KEY", raising=False)
        multiplex_scope({"FIRECRAWL_API_KEY": "scoped-key"})

        from plugins.browser.firecrawl.provider import FirecrawlBrowserProvider

        provider = FirecrawlBrowserProvider()
        assert provider.is_available() is True
        assert provider._headers()["Authorization"] == "Bearer scoped-key"

    def test_firecrawl_scoped_miss_does_not_borrow_environ(
        self, multiplex_scope, monkeypatch
    ):
        monkeypatch.setenv("FIRECRAWL_API_KEY", "env-other-profile")
        multiplex_scope({})

        from plugins.browser.firecrawl.provider import FirecrawlBrowserProvider

        assert FirecrawlBrowserProvider().is_available() is False
