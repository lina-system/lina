"""Matrix standalone sender HTTP contract."""
import asyncio
import os
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch
from plugins.platforms.matrix.adapter import _standalone_send as _matrix_standalone_send
from tests.tools.test_send_message_missing_platforms import _make_aiohttp_resp, _make_aiohttp_session

async def _send_matrix(token, extra, chat_id, message):
    """Pre-migration ``(token, extra, chat_id, message)`` shim around the matrix
    plugin's ``_standalone_send(pconfig, chat_id, message)``."""
    pconfig = SimpleNamespace(token=token, extra=extra or {})
    return await _matrix_standalone_send(pconfig, chat_id, message)

class TestSendMatrix:
    def test_success(self):
        resp = _make_aiohttp_resp(200, json_data={"event_id": "$abc123"})
        session_ctx, session = _make_aiohttp_session(resp)

        with patch("aiohttp.ClientSession", return_value=session_ctx), \
             patch.dict(os.environ, {"MATRIX_HOMESERVER": "", "MATRIX_ACCESS_TOKEN": ""}, clear=False):
            extra = {"homeserver": "https://matrix.example.com"}
            result = asyncio.run(_send_matrix("syt_tok", extra, "!room:example.com", "hello matrix"))

        assert result == {
            "success": True,
            "platform": "matrix",
            "chat_id": "!room:example.com",
            "message_id": "$abc123",
        }
        session.put.assert_called_once()
        call_kwargs = session.put.call_args
        url = call_kwargs[0][0]
        assert url.startswith("https://matrix.example.com/_matrix/client/v3/rooms/%21room%3Aexample.com/send/m.room.message/")
        assert call_kwargs[1]["headers"]["Authorization"] == "Bearer syt_tok"
        payload = call_kwargs[1]["json"]
        assert payload["msgtype"] == "m.text"
        assert payload["body"] == "hello matrix"


    def test_txn_id_is_unique_across_calls(self):
        """Each call should generate a distinct transaction ID in the URL."""
        txn_ids = []

        def capture(*args, **kwargs):
            url = args[0]
            txn_ids.append(url.rsplit("/", 1)[-1])
            ctx = MagicMock()
            ctx.__aenter__ = AsyncMock(return_value=_make_aiohttp_resp(200, json_data={"event_id": "$x"}))
            ctx.__aexit__ = AsyncMock(return_value=False)
            return ctx

        session = MagicMock()
        session.put = capture
        session_ctx = MagicMock()
        session_ctx.__aenter__ = AsyncMock(return_value=session)
        session_ctx.__aexit__ = AsyncMock(return_value=False)

        extra = {"homeserver": "https://matrix.example.com"}

        with patch("aiohttp.ClientSession", return_value=session_ctx):
            asyncio.run(_send_matrix("tok", extra, "!r:example.com", "first"))
        with patch("aiohttp.ClientSession", return_value=session_ctx):
            asyncio.run(_send_matrix("tok", extra, "!r:example.com", "second"))

        assert len(txn_ids) == 2
        assert txn_ids[0] != txn_ids[1]
