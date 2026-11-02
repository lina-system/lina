"""Plugin data paths follow the active profile's HERMES_HOME, including the ContextVar override.

Several plugins carried a ``~/.hermes`` fallback (guarding an ImportError of ``hermes_constants``
that cannot happen for a bundled plugin) or resolved the home at import time. Both are wrong on
Windows and under multiplex profile overrides. Every resolver below must land inside the override.
"""
from __future__ import annotations

import pytest

from hermes_constants import reset_hermes_home_override, set_hermes_home_override


def _discord_recovery(home):
    from plugins.platforms.discord.recovery import DiscordRecoveryStore
    return DiscordRecoveryStore().path()


_RESOLVERS = {"discord": _discord_recovery}

@pytest.mark.parametrize("name", sorted(_RESOLVERS))
def test_plugin_path_follows_profile_override(name, tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / "default"))
    monkeypatch.setenv("HOME", str(tmp_path / "user-home"))
    profile = tmp_path / "profiles" / "b"
    profile.mkdir(parents=True)
    token = set_hermes_home_override(profile)
    try:
        resolved = str(_RESOLVERS[name](profile))
    finally:
        reset_hermes_home_override(token)
    assert resolved.startswith(str(profile)), f"{name} resolved {resolved!r} outside the active profile"
