"""Shared live and fallback profile catalog contracts, exercised through OCX."""

from hermes_cli.models import provider_model_ids

def test_nebius_model_catalog_prefers_live_profile_fetch(monkeypatch):
    from providers import get_provider_profile

    profile = get_provider_profile("ocx")
    assert profile is not None
    monkeypatch.setattr(profile, "fallback_models", ("deepseek-ai/DeepSeek-V4-Pro", "NousResearch/Hermes-4-70B"))
    monkeypatch.setattr(
        "hermes_cli.auth.resolve_api_key_provider_credentials",
        lambda provider_id: {
            "provider": provider_id,
            "api_key": "nebius-live-key",
            "base_url": "http://127.0.0.1:10100/v1",
            "source": "NEBIUS_API_KEY",
        },
    )
    monkeypatch.setattr(
        profile,
        "fetch_models",
        lambda *, api_key=None, base_url=None, timeout=8.0: [
            "deepseek-ai/DeepSeek-V4-Pro",
            "NousResearch/Hermes-4-70B",
            "some-brand-new/Live-Only-Model",
        ],
    )

    # Current merge policy (658ac1d86 / #46309): curated-first for single
    # providers — the profile's fallback_models lead the picker, live-only
    # entries are appended after, and live duplicates of curated entries
    # are deduped rather than repeated.
    result = provider_model_ids("ocx")
    assert result[: len(profile.fallback_models)] == list(profile.fallback_models)
    assert result[len(profile.fallback_models) :] == ["some-brand-new/Live-Only-Model"]


def test_nebius_model_catalog_falls_back_to_profile_models(monkeypatch):
    from providers import get_provider_profile

    profile = get_provider_profile("ocx")
    assert profile is not None
    monkeypatch.setattr(
        "hermes_cli.auth.resolve_api_key_provider_credentials",
        lambda provider_id: {
            "provider": provider_id,
            "api_key": "nebius-live-key",
            "base_url": "http://127.0.0.1:10100/v1",
            "source": "NEBIUS_API_KEY",
        },
    )
    monkeypatch.setattr(profile, "fetch_models", lambda *, api_key=None, base_url=None, timeout=8.0: None)

    assert provider_model_ids("opencodex") == list(profile.fallback_models)
