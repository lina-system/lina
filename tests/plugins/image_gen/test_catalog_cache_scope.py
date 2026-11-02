"""Image catalogs are isolated by credentials."""

from tests.plugins.test_multiplex_plugin_cache_scope import _Resp

def test_credentialed_catalog_probe_failure_is_not_cached_across_keys(monkeypatch):
    """A 401 under one key must not pin a sibling profile (same base URL, valid key) to the empty
    catalog for the TTL."""
    import requests

    import plugins.image_gen.openrouter as orp

    def fake_get(url, headers=None, timeout=None, **_kw):
        if (headers or {}).get("Authorization") == "Bearer good-key":
            return _Resp({"data": [{"id": "google/gemini-image"}]})
        return _Resp({"error": "unauthorized"}, status=401)

    monkeypatch.setattr(requests, "get", fake_get)
    orp._CATALOG_CACHE.clear()
    try:
        assert orp._fetch_image_api_catalog("https://openrouter.ai/api/v1", "bad-key") == frozenset()
        assert "google/gemini-image" in orp._fetch_image_api_catalog("https://openrouter.ai/api/v1", "good-key")
    finally:
        orp._CATALOG_CACHE.clear()
