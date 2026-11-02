"""Platform authorization reads must honor the per-profile secret scope (#93522).

Under ``gateway.multiplex_profiles`` every secondary profile is constructed
inside ``_profile_runtime_scope`` and its ``.env`` lives in that scope —
``gateway/run.py`` explicitly does NOT mutate ``os.environ`` with it. Raw
``os.getenv`` authorization reads therefore (a) silently miss the secondary
profile's own allowlists (fail-closed no-replies) and (b) leak the default
profile's ``GATEWAY_ALLOW_ALL_USERS`` / allowlists into secondary profiles
(fail-open admissions).

Covers the scoped env helpers of Telegram / Photon plus the
``_open_dm_opted_in`` gates built on them. Canonical shape reference:
``gateway.platforms._shared.get_scoped_secret``.
"""

import contextlib
import os

import pytest

from agent.secret_scope import reset_secret_scope, set_secret_scope


@contextlib.contextmanager
def _scope(secrets):
    token = set_secret_scope(secrets)
    try:
        yield
    finally:
        reset_secret_scope(token)


@pytest.mark.parametrize(
    "module_path,helper_name,var",
    [
        ("plugins.platforms.telegram.adapter", "_get_scoped_secret", "TELEGRAM_ALLOWED_USERS"),
        ("plugins.platforms.photon.adapter", "_get_scoped_secret", "PHOTON_ALLOWED_USERS"),
    ],
)
def test_helper_reads_profile_scope_first(module_path, helper_name, var, monkeypatch):
    """Scope value wins over anything in os.environ."""
    monkeypatch.setenv(var, "FROM-DEFAULT-ENV")
    module = __import__(module_path, fromlist=[helper_name])
    helper = getattr(module, helper_name)

    with _scope({var: "from-profile-scope"}):
        assert helper(var, "") == "from-profile-scope"


@pytest.fixture
def multiplex_on(monkeypatch):
    """Simulate a multiplexed deployment: scoped misses fail closed.

    With multiplexing off, a scope miss deliberately falls through to
    os.environ (the cron-overlay contract) — the cross-profile leak only
    exists while the multiplexer is active, which is exactly the #93522
    scenario.
    """
    monkeypatch.setattr("agent.secret_scope._MULTIPLEX_ACTIVE", True)


@pytest.mark.parametrize(
    "module_path,helper_name",
    [
        ("plugins.platforms.telegram.adapter", "_get_scoped_secret"),
        ("plugins.platforms.photon.adapter", "_get_scoped_secret"),
    ],
)
def test_helper_does_not_leak_default_env_into_scoped_miss(
    module_path, helper_name, multiplex_on, monkeypatch
):
    """Scoped miss must return the default, NOT fall through to os.environ.

    This is the fail-closed half of #93522: the default profile's
    ``*_ALLOW_ALL_USERS=true`` in process env must not answer a secondary
    profile's gate.
    """
    monkeypatch.setenv("TELEGRAM_ALLOW_ALL_USERS", "true")
    monkeypatch.setenv("PHOTON_ALLOW_ALL_USERS", "true")
    monkeypatch.setenv("TELEGRAM_ALLOWED_USERS", "*")
    monkeypatch.setenv("DISCORD_ALLOW_ALL_USERS", "true")

    module = __import__(module_path, fromlist=[helper_name])
    helper = getattr(module, helper_name)

    with _scope({}):  # scope installed, keys absent
        assert helper("TELEGRAM_ALLOW_ALL_USERS", "") == ""
        assert helper("PHOTON_ALLOW_ALL_USERS", "") == ""
        assert helper("DISCORD_ALLOW_ALL_USERS", "") == ""
        # The scoped read must not see an unscoped wildcard:
        assert helper("TELEGRAM_ALLOWED_USERS", "restricted-default") == "restricted-default"


@pytest.mark.parametrize(
    "module_path,helper_name,var",
    [
        ("plugins.platforms.telegram.adapter", "_get_scoped_secret", "TELEGRAM_ALLOWED_USERS"),
        ("plugins.platforms.photon.adapter", "_get_scoped_secret", "PHOTON_ALLOWED_USERS"),
    ],
)
def test_helper_falls_back_to_environ_without_scope(module_path, helper_name, var, monkeypatch):
    """Single-profile (no scope installed): os.environ remains authoritative."""
    monkeypatch.setenv(var, "legacy-env-value")
    module = __import__(module_path, fromlist=[helper_name])
    helper = getattr(module, helper_name)
    assert helper(var, "") == "legacy-env-value"
