"""A profile name never leads ``create_profile`` (and the store lifecycle it starts) outside the profiles root."""

from pathlib import Path

import pytest

from hermes_cli.profiles import create_profile


@pytest.fixture
def profile_env(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setenv("HERMES_HOME", str(home))
    return tmp_path


@pytest.mark.parametrize("name", ["..", "../escape", "../../escape", "a/b", "a\\b", "profiles/../../escape"])
def test_a_name_with_dots_or_a_separator_is_refused_before_anything_is_written(profile_env, name):
    before = sorted(p.relative_to(profile_env) for p in profile_env.rglob("*"))

    with pytest.raises(ValueError, match="not a valid profile name"):
        create_profile(name, no_alias=True)

    assert sorted(p.relative_to(profile_env) for p in profile_env.rglob("*")) == before
