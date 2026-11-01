"""Photon paths follow the active profile."""
import pytest
from hermes_constants import reset_hermes_home_override, set_hermes_home_override
def _photon_auth(home):
    from plugins.platforms.photon import auth
    return auth._auth_json_path()



_RESOLVERS = {"photon": _photon_auth}

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
