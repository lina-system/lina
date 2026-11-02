"""A plain audio MEDIA file (``is_voice=False``: music, a sound effect) is never sent as a voice note.

The media dispatch routes every audio extension to ``send_voice(..., is_voice=<bool>)``; adapters
whose ``send_voice`` is a native voice-note transport must honour the flag. Each case records the
adapter's own wire boundary.
"""
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock

import pytest

from gateway.config import PlatformConfig


@pytest.mark.asyncio
@pytest.mark.parametrize("is_voice, audio_message", [(False, False), (None, True)], ids=["plain", "voice"])
async def test_bluebubbles_sends_plain_audio_without_the_voice_memo_flag(tmp_path, monkeypatch, is_voice, audio_message):
    from plugins.platforms.bluebubbles.adapter import BlueBubblesAdapter

    adapter = BlueBubblesAdapter(PlatformConfig(enabled=True, extra={"server_url": "http://localhost:1234", "password": "x"}))
    response = MagicMock()
    response.json.return_value = {"status": 200, "data": {"guid": "g1"}}
    adapter.client = SimpleNamespace(post=AsyncMock(return_value=response))
    monkeypatch.setattr(adapter, "_resolve_chat_guid", AsyncMock(return_value="iMessage;-;+15551234567"))
    path = tmp_path / "door_knock.mp3"
    path.write_bytes(b"ID3\x04" + b"\x00" * 32)

    assert (await adapter.send_voice("+15551234567", str(path), is_voice=is_voice)).success
    data = adapter.client.post.await_args.kwargs["data"]
    assert ("isAudioMessage" in data) is audio_message
    assert data["name"] == "door_knock.mp3"
