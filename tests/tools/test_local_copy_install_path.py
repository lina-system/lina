"""A migrated copy's terminal resolves ``lina`` and ``hermes`` to the copy's own install, even when the login
shell's profile puts an old wrapper first; every other profile keeps the login PATH's order (lina#719)."""

import sys

import pytest

from tests._fixtures.copy_install import login_profile_puts_first, old_wrapper, terminal_resolves, venv_install
from tools.environments import local


def _profile(tmp_path, monkeypatch, home_mode: str | None):
    """A profile run the way the CLI runs it: HERMES_HOME, and ``terminal.home_mode`` bridged to the env."""
    install = venv_install(tmp_path / "install")
    wrapper = old_wrapper(tmp_path / "user/.local/bin")
    profile = tmp_path / "lina/profiles/alpha"
    # Whichever HOME the terminal gets, its login profile puts the wrapper first.
    for home in (tmp_path / "user", profile / "home"):
        login_profile_puts_first(home, wrapper)
    monkeypatch.setattr(sys, "executable", str(install / "python"))
    monkeypatch.setenv("HOME", str(tmp_path / "user"))
    monkeypatch.setenv("HERMES_HOME", str(profile))
    if home_mode is not None:
        monkeypatch.setenv("TERMINAL_HOME_MODE", home_mode)
    # The process PATH lists the wrapper too, as the trial gateway's did.
    monkeypatch.setenv("PATH", f"{wrapper}:/usr/local/bin:/usr/bin:/bin")
    monkeypatch.setattr(local, "_HERMES_BIN_DIR", local._SENTINEL)
    return install, wrapper, profile


@pytest.mark.platforms("posix")
def test_copy_terminal_resolves_its_own_install_first(tmp_path, monkeypatch):
    install, _, profile = _profile(tmp_path, monkeypatch, "profile")

    assert terminal_resolves(profile, "lina", "hermes") == [str(install / "lina"), str(install / "hermes")]


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("home_mode", [None, "auto", "real"])
def test_other_profiles_keep_the_login_path_order(tmp_path, monkeypatch, home_mode):
    _, wrapper, profile = _profile(tmp_path, monkeypatch, home_mode)

    assert terminal_resolves(profile, "lina", "hermes") == [str(wrapper / "lina"), str(wrapper / "hermes")]
    assert local._prepend_hermes_bin_dir(f"/usr/bin:{wrapper}:/bin") == f"/usr/bin:{wrapper}:/bin"
