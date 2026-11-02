"""Profile-declared reasoning vocabularies on the shared Codex transport."""

import pytest

from agent.transports import get_transport


@pytest.fixture
def transport():
    import agent.transports.codex
    return get_transport("codex_responses")


@pytest.fixture
def seeded_catalog(monkeypatch):
    """Register a synthetic catalog vocabulary on the real profile lookup path."""
    import providers
    from providers.base import ProviderProfile

    class CatalogProfile(ProviderProfile):
        def supported_reasoning_efforts(self, model):
            return {
                "grok-4.6": ("minimal", "low", "medium", "high", "xhigh"),
                "gpt-4.1-mini": (),
                "accounts/fireworks/models/kimi-k3": (
                    "minimal", "low", "medium", "high", "xhigh", "max",
                ),
            }.get(model)

    providers.get_provider_profile("ocx")
    monkeypatch.setattr(providers, "_REGISTRY", dict(providers._REGISTRY))
    providers.register_provider(CatalogProfile(
        name="catalog-test", hostname="catalog.example", hostnames=("catalog.example",),
    ))
    return providers.get_provider_profile("catalog-test")


class TestTransportClamp:
    def _kwargs(self, transport, model, reasoning_config=None):
        return transport.build_kwargs(
            model=model,
            messages=[{"role": "user", "content": "Hi"}],
            tools=[],
            base_url="https://catalog.example/v1",
            session_id="sid",
            provider="catalog-test",
            reasoning_config=reasoning_config,
        )

    def test_clamps_to_catalog_vocabulary(self, transport, seeded_catalog):
        # grok-4.6 via Router has no "max" — nearest weaker supported is xhigh.
        kw = self._kwargs(transport, "grok-4.6", {"effort": "max"})
        assert kw["reasoning"]["effort"] == "xhigh"

    def test_supported_effort_passes_through(self, transport, seeded_catalog):
        kw = self._kwargs(
            transport, "accounts/fireworks/models/kimi-k3", {"effort": "max"}
        )
        assert kw["reasoning"]["effort"] == "max"

    def test_non_reasoning_model_suppresses_reasoning(self, transport, seeded_catalog):
        # Default reasoning_config is enabled — the () verdict must strip the
        # reasoning field entirely (Router 400s rather than ignoring it).
        kw = self._kwargs(transport, "gpt-4.1-mini")
        assert "reasoning" not in kw
        assert kw.get("include") == []

    def test_unknown_model_falls_back_to_codex_default(self, transport, seeded_catalog):
        # Not in the catalog -> default codex vocabulary applies (legacy has
        # xhigh but no max: max clamps to xhigh, medium is untouched).
        kw = self._kwargs(transport, "some-byok-route", {"effort": "max"})
        assert kw["reasoning"]["effort"] == "xhigh"
        kw = self._kwargs(transport, "some-byok-route", {"effort": "medium"})
        assert kw["reasoning"]["effort"] == "medium"

    def test_cold_cache_keeps_default_behavior(self, transport, seeded_catalog, monkeypatch):
        monkeypatch.setattr(seeded_catalog, "supported_reasoning_efforts", lambda model: None)
        kw = self._kwargs(transport, "grok-4.6", {"effort": "xhigh"})
        assert kw["reasoning"]["effort"] == "xhigh"

    def test_other_providers_unaffected(self, transport, seeded_catalog):
        kw = transport.build_kwargs(
            model="gpt-4.1-mini",
            messages=[{"role": "user", "content": "Hi"}],
            tools=[],
            base_url="https://generic.example.com/v1",
            session_id="sid",
            provider="some-other-provider",
            reasoning_config={"effort": "medium"},
        )
        # The router catalog's () verdict for gpt-4.1-mini must not leak
        # into other providers' requests.
        assert kw["reasoning"]["effort"] == "medium"


class TestHostResolvedProfile:
    def test_named_custom_provider_at_router_host_gets_the_clamp(
        self, transport, seeded_catalog
    ):
        # A providers.my-proxy entry pointed at api.router.com rides the same
        # host mandate onto this transport under the runtime's ``custom:<name>``
        # identity; the vocabulary must follow the host, not the config-entry
        # name — and not the generic custom declaration either.
        kw = transport.build_kwargs(
            model="grok-4.6",
            messages=[{"role": "user", "content": "Hi"}],
            tools=[],
            base_url="https://catalog.example/v1",
            session_id="sid",
            provider="custom:my-proxy",
            reasoning_config={"effort": "max"},
        )
        assert kw["reasoning"]["effort"] == "xhigh"

    def test_foreign_host_does_not_borrow_the_router_vocabulary(
        self, transport, seeded_catalog
    ):
        kw = transport.build_kwargs(
            model="grok-4.6",
            messages=[{"role": "user", "content": "Hi"}],
            tools=[],
            base_url="https://generic.example.com/v1",
            session_id="sid",
            provider="my-proxy",
            reasoning_config={"effort": "max"},
        )
        # Default codex vocabulary applies (legacy: no max -> xhigh).
        assert kw["reasoning"]["effort"] == "xhigh"
