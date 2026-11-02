"""Every platform adapter's ``send_voice`` must accept the ``is_voice`` keyword the base media
dispatch passes (``BasePlatformAdapter._deliver_media_from_response`` →
``send_voice(..., is_voice=is_voice)``). An explicit signature without it raises ``TypeError`` and
the attachment is silently dropped (#102221, #116776 — Matrix; same class in line/bluebubbles/weixin)."""

from __future__ import annotations

import pytest


@pytest.mark.asyncio
async def test_media_dispatch_delivers_audio_through_bluebubbles_send_voice(tmp_path) -> None:
    """Behavioural pin for the ``**kwargs`` class fix (line/bluebubbles/weixin): drive the real
    base dispatch, which calls ``send_voice(..., is_voice=False)`` for an audio-ext MEDIA
    attachment. Before the fix the call raised ``TypeError`` inside ``_send_one`` and the
    attachment was recorded as a failed delivery; now the file reaches the adapter's uploader."""
    from unittest.mock import AsyncMock

    from gateway.config import PlatformConfig
    from gateway.platforms.base import SendResult
    from gateway.platforms.event import MessageEvent
    from gateway.session import Platform, SessionSource
    from plugins.platforms.bluebubbles.adapter import BlueBubblesAdapter

    adapter = BlueBubblesAdapter(PlatformConfig(enabled=True, extra={"server_url": "http://localhost:1234", "password": "x"}))
    adapter._send_attachment = AsyncMock(return_value=SendResult(success=True, message_id="post-1"))
    adapter._notify_media_delivery_failure = AsyncMock()
    clip = tmp_path / "clip.mp3"
    clip.write_bytes(b"ID3")
    event = MessageEvent(text="", source=SessionSource(platform=Platform.BLUEBUBBLES, chat_id="chan-1"))
    results = []

    await adapter._deliver_media_attachments(
        event, [(str(clip), False)], [], force_document_attachments=False, human_delay=0,
        metadata={}, record_delivery=results.append)

    assert [r.success for r in results] == [True]
    adapter._notify_media_delivery_failure.assert_not_awaited()
    args, _kwargs = adapter._send_attachment.call_args
    assert args[1] == str(clip)
    assert _kwargs["is_audio_message"] is False
