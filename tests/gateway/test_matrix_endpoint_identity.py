"""Matrix endpoint credentials share the active scope."""
import pytest
from gateway.config import PlatformConfig
from tests.gateway.test_multiplex_endpoint_identity_scope import secondary_scope, DEFAULT_ENV, SECONDARY

pytestmark = pytest.mark.usefixtures("secondary_scope")

def test_matrix_homeserver_identity_follow_the_scoped_token():
    from plugins.platforms.matrix.adapter import MatrixAdapter

    adapter = MatrixAdapter(PlatformConfig(enabled=True))
    assert adapter._access_token == SECONDARY["MATRIX_ACCESS_TOKEN"]
    assert (adapter._homeserver, adapter._user_id, adapter._device_id) == (
        SECONDARY["MATRIX_HOMESERVER"], SECONDARY["MATRIX_USER_ID"], SECONDARY["MATRIX_DEVICE_ID"])


@pytest.mark.asyncio
async def test_matrix_standalone_send_posts_scoped_token_to_scoped_homeserver(monkeypatch):
    import aiohttp
    from plugins.platforms.matrix import adapter as mx

    seen = {}

    class _Resp:
        status = 200
        async def __aenter__(self): return self
        async def __aexit__(self, *a): return False
        async def json(self): return {"event_id": "$x"}
        async def text(self): return ""

    class _Sess:
        def __init__(self, *a, **k): pass
        async def __aenter__(self): return self
        async def __aexit__(self, *a): return False
        def put(self, url, **kw): seen["url"] = url; seen["headers"] = kw.get("headers", {}); return _Resp()

    monkeypatch.setattr(aiohttp, "ClientSession", _Sess)
    await mx._standalone_send(PlatformConfig(enabled=True), "!room:example", "hi")
    assert seen["url"].startswith(SECONDARY["MATRIX_HOMESERVER"] + "/")
    assert DEFAULT_ENV["MATRIX_HOMESERVER"] not in seen["url"]
