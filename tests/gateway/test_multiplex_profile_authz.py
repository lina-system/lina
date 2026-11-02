"""Regression tests for multiplex profile-aware own-policy authorization."""

from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock

import pytest

from gateway.config import GatewayConfig, Platform, PlatformConfig
from gateway.session import SessionSource


def _clear_auth_env(monkeypatch) -> None:
    for key in (
        "TELEGRAM_ALLOWED_USERS",
        "GATEWAY_ALLOWED_USERS",
        "GATEWAY_ALLOW_ALL_USERS",
        "TELEGRAM_ALLOW_ALL_USERS",
    ):
        monkeypatch.delenv(key, raising=False)


def _make_multiplex_runner(monkeypatch):
    """Runner with default allowlist Telegram and secondary open-policy Telegram."""
    from gateway.run import GatewayRunner

    _clear_auth_env(monkeypatch)

    runner = object.__new__(GatewayRunner)
    runner.config = GatewayConfig(multiplex_profiles=True)

    default_adapter = SimpleNamespace(
        send=AsyncMock(),
        enforces_own_access_policy=True,
        _dm_policy="allowlist",
        _group_policy="pairing",
    )
    secondary_adapter = SimpleNamespace(
        send=AsyncMock(),
        enforces_own_access_policy=True,
        _dm_policy="open",
        _group_policy="open",
    )

    runner.adapters = {Platform.TELEGRAM: default_adapter}
    runner._profile_adapters = {
        "coder": {Platform.TELEGRAM: secondary_adapter},
    }
    runner.pairing_store = MagicMock()
    runner.pairing_store.is_approved.return_value = False
    return runner, default_adapter, secondary_adapter


def test_default_profile_still_trusts_own_allowlist(monkeypatch):
    """Default-profile allowlist trust is unchanged when profile is unstamped."""
    runner, _default_adapter, _secondary_adapter = _make_multiplex_runner(monkeypatch)

    source = SessionSource(
        platform=Platform.TELEGRAM,
        user_id="allowed-user",
        chat_id="dm-chat",
        user_name="allowed-user",
        chat_type="dm",
        profile=None,
    )

    assert runner._is_user_authorized(source) is True


def test_active_profile_stamp_resolves_primary_adapter(monkeypatch):
    """A single-profile gateway stamps its active profile but stores adapters as primary."""
    runner, default_adapter, _secondary_adapter = _make_multiplex_runner(monkeypatch)
    runner._active_profile_name = lambda: "dev"

    assert runner._authorization_adapter(Platform.TELEGRAM, profile="dev") is default_adapter


def test_scoped_secondary_profile_still_uses_profile_adapters(monkeypatch):
    """Runtime scope must not redirect secondary authz to primary adapters.

    ``_make_profile_message_handler`` wraps ``_handle_message`` in
    ``_profile_runtime_scope``, which overrides HERMES_HOME so
    ``get_active_profile_name()`` equals the secondary profile for that turn.
    Authorization must still read ``_profile_adapters[profile]``, not the
    empty primary ``self.adapters`` map — otherwise upstream-auth platforms
    such as A2A default-deny an already-authenticated peer (#80884). A
    secondary profile with NO registry entry still fails closed.
    """
    from gateway.run import GatewayRunner

    _clear_auth_env(monkeypatch)

    runner = object.__new__(GatewayRunner)
    runner.config = GatewayConfig(multiplex_profiles=True)
    runner.adapters = {}
    runner.pairing_store = MagicMock()
    runner.pairing_store.is_approved.return_value = False

    secondary = SimpleNamespace(
        authorization_is_upstream=True,
        enforces_own_access_policy=False,
    )
    runner._profile_adapters = {"beta": {Platform.API_SERVER: secondary}}
    # Simulate the scoped turn: active profile name collapses to the secondary.
    runner._active_profile_name = lambda: "beta"

    assert runner._authorization_adapter(Platform.API_SERVER, profile="beta") is secondary

    source = SessionSource(
        platform=Platform.API_SERVER,
        chat_id="api-context",
        user_id="alpha",
        user_name="alpha",
        chat_type="dm",
        profile="beta",
    )
    assert runner._is_user_authorized(source) is True

    # Fail-closed guard is untouched: no registry entry -> no default fallback.
    runner._profile_adapters = {"beta": {}}
    assert runner._authorization_adapter(Platform.API_SERVER, profile="beta") is None


def test_secondary_allowlist_dm_behavior_ignores_unauthorized(monkeypatch):
    """Unauthorized-DM behavior must read the secondary adapter's dm_policy."""
    runner, _default_adapter, secondary_adapter = _make_multiplex_runner(monkeypatch)
    secondary_adapter._dm_policy = "allowlist"

    assert runner._get_unauthorized_dm_behavior(
        Platform.TELEGRAM,
        profile="coder",
    ) == "ignore"
    assert runner._get_unauthorized_dm_behavior(Platform.TELEGRAM) == "ignore"


def test_adapter_auth_check_stamps_secondary_profile(monkeypatch):
    """The adapter auth-check callback must stamp its own secondary profile.

    Regression for the gap where ``_make_adapter_auth_check`` built a
    profile-less ``SessionSource``, so a secondary adapter's external-context
    authorization (e.g. Slack/Discord thread-reply lookups) silently
    resolved the *active* profile's allowlist scope instead of its own.
    """
    from gateway.run import GatewayRunner

    _clear_auth_env(monkeypatch)

    runner = object.__new__(GatewayRunner)
    runner.config = GatewayConfig(multiplex_profiles=True)

    captured: dict = {}

    def fake_is_user_authorized(source):
        captured["profile"] = source.profile
        return True

    runner._is_user_authorized = fake_is_user_authorized

    check = runner._make_adapter_auth_check(Platform.TELEGRAM, profile_name="coder")
    assert check("some-user", "dm", "dm-chat") is True
    assert captured["profile"] == "coder"


_PHOTON = Platform("photon")


def _make_photon_multiplex_runner(monkeypatch, extra):
    """Runner whose secondary 'coder' profile runs a live Photon adapter."""
    from gateway.run import GatewayRunner

    for key in (
        "PHOTON_ALLOWED_USERS",
        "PHOTON_ALLOW_ALL_USERS",
        "GATEWAY_ALLOWED_USERS",
        "GATEWAY_ALLOW_ALL_USERS",
    ):
        monkeypatch.delenv(key, raising=False)

    runner = object.__new__(GatewayRunner)
    runner.config = GatewayConfig(multiplex_profiles=True)

    adapter = SimpleNamespace(
        config=PlatformConfig(enabled=True, extra=extra),
        # Exercise the shared normalization hook independently of wire formats.
        normalize_user_id=lambda value: str(value).strip().lower(),
    )
    runner.adapters = {}
    runner._profile_adapters = {"coder": {_PHOTON: adapter}}
    runner.pairing_store = MagicMock()
    runner.pairing_store.is_approved.return_value = False
    return runner


def _photon_source(user_id):
    return SessionSource(
        platform=_PHOTON,
        user_id=user_id,
        chat_id="chat-1",
        user_name="member",
        chat_type="dm",
        profile="coder",
    )


def _patch_photon_registry(monkeypatch, allowed_users_env="PHOTON_ALLOWED_USERS"):
    from gateway.platform_registry import platform_registry

    real_get = platform_registry.get

    def _get(key):
        if key == "photon":
            return SimpleNamespace(allowed_users_env=allowed_users_env)
        return real_get(key)

    monkeypatch.setattr(platform_registry, "get", _get)


def test_secondary_photon_extra_allowed_users_authorizes_listed_user(monkeypatch):
    """A secondary profile's extra.allowed_users must authorize its users when
    the env var only ever carried the default profile's list (#98738/#82871)."""

    runner = _make_photon_multiplex_runner(
        monkeypatch, extra={"allowed_users": ["member"]}
    )
    _patch_photon_registry(monkeypatch)

    assert runner._is_user_authorized(_photon_source("member")) is True


def test_secondary_photon_extra_allowed_users_denies_unlisted_sender(monkeypatch):
    """Default-deny is preserved: a sender not in the profile's allowlist
    stays denied even though the adapter-level list admitted the message."""

    runner = _make_photon_multiplex_runner(
        monkeypatch, extra={"allowed_users": ["other" + "b" * 56]}
    )
    _patch_photon_registry(monkeypatch)

    assert runner._is_user_authorized(_photon_source("member")) is False


def test_secondary_photon_without_extra_allowlist_stays_default_deny(monkeypatch):
    """No extra.allowed_users configured: nothing changes, the default-deny
    path applies (no fail-open via an empty list)."""

    runner = _make_photon_multiplex_runner(monkeypatch, extra={})
    _patch_photon_registry(monkeypatch)

    assert runner._is_user_authorized(_photon_source("member")) is False


def test_extra_allowed_users_not_consulted_without_registry_declaration(monkeypatch):
    """The fallback is gated on the platform's registry entry declaring
    allowed_users_env — a platform without that contract keeps the previous
    behavior even if its extra happens to hold an allowed_users key."""

    runner = _make_photon_multiplex_runner(
        monkeypatch, extra={"allowed_users": ["someone"]}
    )
    _patch_photon_registry(monkeypatch, allowed_users_env="")

    assert runner._is_user_authorized(_photon_source("someone")) is False


def test_extra_allowed_users_wildcard_authorizes_any_sender(monkeypatch):
    """\"*\" in the profile's extra.allowed_users keeps the env-var wildcard
    semantics: any sender is authorized (still gated on the registry
    declaration)."""

    runner = _make_photon_multiplex_runner(monkeypatch, extra={"allowed_users": ["*"]})
    _patch_photon_registry(monkeypatch)

    assert runner._is_user_authorized(_photon_source("member")) is True


def test_extra_allowed_users_blank_entries_are_dropped_not_denials(monkeypatch):
    """Blank/whitespace entries are dropped at parse; an otherwise-empty
    list behaves like the absent case (default-deny), not like a wildcard."""

    runner = _make_photon_multiplex_runner(
        monkeypatch, extra={"allowed_users": ["", "   ", ","]}
    )
    _patch_photon_registry(monkeypatch)

    assert runner._is_user_authorized(_photon_source("member")) is False
