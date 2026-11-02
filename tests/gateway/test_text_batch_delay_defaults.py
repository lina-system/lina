"""Telegram text-batch configuration is clamped to the shared ceilings."""
from gateway.config import PlatformConfig
from plugins.platforms.telegram.adapter import TelegramAdapter


def test_text_batch_delays_clamped_to_shared_ceilings(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.setenv("HERMES_TELEGRAM_TEXT_BATCH_DELAY_SECONDS", "2.5")
    monkeypatch.setenv("HERMES_TELEGRAM_TEXT_BATCH_SPLIT_DELAY_SECONDS", "7")
    adapter = TelegramAdapter(PlatformConfig(enabled=True, token="test-token"))

    assert adapter._text_batch_delay_seconds == adapter._TEXT_BATCH_MAX_DELAY_S
    assert adapter._text_batch_split_delay_seconds == adapter._TEXT_BATCH_MAX_SPLIT_DELAY_S
