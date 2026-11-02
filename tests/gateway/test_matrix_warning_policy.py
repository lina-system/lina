"""Matrix diagnostics preserve captions and receipts."""
from types import SimpleNamespace
from unittest.mock import AsyncMock
import pytest
from gateway.config import PlatformConfig
from plugins.platforms.matrix.adapter import MatrixAdapter
from tests.gateway import test_native_warning_coverage as shared

policy = shared.policy

@pytest.mark.asyncio
@pytest.mark.parametrize("caption", [None, "Error: this is the requested caption"])
async def test_matrix_download_diagnostic_not_caption_is_suppressed(policy, caption, monkeypatch, caplog):
    adapter = MatrixAdapter(PlatformConfig())
    frames = []

    async def record(room, event_type, content):
        frames.append(content)
        return "$sent"

    adapter._client = SimpleNamespace(send_message_event=record)
    adapter._download_external_media_with_cap = AsyncMock(side_effect=RuntimeError("download fixture"))
    monkeypatch.setattr("tools.url_safety.is_safe_url", lambda url: True)
    result = await adapter.send_image("!room", "https://example.invalid/image?token=private", caption, "$reply", {"thread_id": "$thread"})
    if policy:
        assert [f["body"] for f in frames] == ([caption] if caption else [])
        assert not result.success and result.message_id is None
        if frames:
            assert frames[0]["m.relates_to"]["event_id"] == "$thread"
    else:
        from agent.i18n import t
        notice = t("platform.matrix.media.image_download_failed")
        assert [f["body"] for f in frames] == [f"{caption}\n{notice}" if caption else notice]
        assert result.success and result.message_id == "$sent"
    assert "failed to download image" in caplog.text
