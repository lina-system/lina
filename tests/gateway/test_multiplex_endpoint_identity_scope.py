"""Invariant: an adapter's endpoint/identity resolves through the SAME profile scope as its secret.

Under ``gateway.multiplex_profiles`` ``os.environ`` holds the DEFAULT profile's values; a secondary
profile's ``.env`` exists only in its secret scope. Every adapter here already read its *secret*
scope-aware but read the paired homeserver / URL / client_id / from-number / webhook URL raw from
``os.environ`` — so the secondary's credential was sent to the default profile's host or identity.
"""

from __future__ import annotations

import os
from unittest.mock import AsyncMock, MagicMock

import pytest

from agent import secret_scope as ss
from gateway.config import PlatformConfig

DEFAULT_ENV = {
    "TELEGRAM_WEBHOOK_URL": "https://default.example/tg",
}
SECONDARY = {
    "TELEGRAM_WEBHOOK_URL": "https://bot2.example/tg",
}


@pytest.fixture
def secondary_scope(monkeypatch):
    """Default profile in os.environ, multiplex on, secondary profile's scope installed."""
    for key, value in DEFAULT_ENV.items():
        monkeypatch.setenv(key, value)
    ss.set_multiplex_active(True)
    token = ss.set_secret_scope(SECONDARY)
    yield
    ss.reset_secret_scope(token)
    ss.set_multiplex_active(False)


@pytest.mark.asyncio
async def test_telegram_connect_registers_scoped_webhook_url(secondary_scope, monkeypatch):
    from plugins.platforms.telegram import adapter as tg

    class _Builder:
        def __getattr__(self, name):
            return lambda *a, **k: self
        def build(self):
            app = MagicMock(); app.bot = MagicMock(); app.start = AsyncMock(); app.initialize = AsyncMock()
            return app

    monkeypatch.setattr(tg, "Application", type("_App", (), {"builder": staticmethod(_Builder)}))
    adapter = tg.TelegramAdapter(PlatformConfig(enabled=True, token="bot2-token"))
    for name in ("_wire_plugin_handlers", "_register_handlers", "_mark_connected", "_start_post_connect_housekeeping"):
        monkeypatch.setattr(adapter, name, MagicMock())
    monkeypatch.setattr(adapter, "_acquire_platform_lock", lambda *a, **k: True)
    monkeypatch.setattr(adapter, "_build_ptb_requests", AsyncMock(return_value=(MagicMock(), MagicMock())))
    monkeypatch.setattr(adapter, "_initialize_app_with_retries", AsyncMock())
    seen = {}

    async def _webhook(url, *, is_reconnect): seen["url"] = url
    async def _polling(*, is_reconnect): seen["url"] = "<polling>"

    monkeypatch.setattr(adapter, "_start_webhook_mode", _webhook)
    monkeypatch.setattr(adapter, "_start_polling_mode", _polling)
    assert await adapter.connect() is True
    assert seen["url"] == SECONDARY["TELEGRAM_WEBHOOK_URL"]
    assert os.environ["TELEGRAM_WEBHOOK_URL"] == DEFAULT_ENV["TELEGRAM_WEBHOOK_URL"]  # env untouched; scope won
