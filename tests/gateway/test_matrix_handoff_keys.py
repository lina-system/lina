"""Matrix handoff keys match native replies."""
import asyncio
from types import SimpleNamespace
from unittest.mock import AsyncMock
import pytest
from gateway.config import Platform, PlatformConfig
from gateway.session import build_session_key
from tests.gateway.test_handoff_thread_session_key import _handoff_destination

def _matrix_adapter(room_id: str, *, is_dm: bool, monkeypatch):
    """A real MatrixAdapter with a fake client: the room's DM flag comes from the m.direct cache."""
    from plugins.platforms.matrix.adapter import MatrixAdapter

    monkeypatch.setenv("MATRIX_REQUIRE_MENTION", "false")
    monkeypatch.delenv("MATRIX_AUTO_THREAD", raising=False)
    adapter = MatrixAdapter(PlatformConfig(enabled=True, token="syt_test", extra={
        "homeserver": "https://matrix.example.org", "user_id": "@hermes:example.org"}))
    adapter._text_batch_delay_seconds = 0
    adapter._startup_ts = 0
    adapter._dm_rooms[room_id] = is_dm
    adapter._client = SimpleNamespace(send_message_event=AsyncMock(return_value="$seed"), crypto=None)
    adapter._get_display_name = AsyncMock(return_value="alice")
    adapter._background_read_receipt = lambda *_a: None
    adapter.handle_message = AsyncMock()
    return adapter


def _organic_matrix_reply_key(adapter, room_id: str, thread_id: str) -> str:
    """Key the Matrix adapter builds for a human reply inside ``thread_id`` (real inbound path)."""
    import time

    event = SimpleNamespace(
        sender="@alice:example.org", event_id="$reply", room_id=room_id, timestamp=int(time.time() * 1000),
        content={"body": "what happened?", "msgtype": "m.text",
                 "m.relates_to": {"rel_type": "m.thread", "event_id": thread_id}})
    asyncio.run(adapter._on_room_message(event))
    return build_session_key(adapter.handle_message.await_args.args[0].source, thread_sessions_per_user=False)


@pytest.mark.parametrize("is_dm", [False, True], ids=["room", "dm"])
def test_matrix_handoff_key_matches_the_thread_reply_key(is_dm, monkeypatch):
    """A Matrix handoff (and cron ``attach_to_session``) seeds a thread rooted on the seed event; the
    adapter keys replies in it on the ROOM's type (``group``/``dm``), never a ``thread`` slot, so the
    destination must bind that key or the first reply opens an empty session (#112918)."""
    room_id = "!ops:example.org"
    adapter = _matrix_adapter(room_id, is_dm=is_dm, monkeypatch=monkeypatch)
    _dest, handoff = _handoff_destination(Platform.MATRIX, room_id, "$seed", None, adapter)
    assert handoff == _organic_matrix_reply_key(adapter, room_id, "$seed")
    assert handoff.split(":")[3] == ("dm" if is_dm else "group")
