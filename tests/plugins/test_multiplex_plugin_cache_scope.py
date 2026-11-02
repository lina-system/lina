"""Plugin module-level caches must not hand profile A's state to profile B under a multiplexed
HERMES_HOME override (``hermes_constants.set_hermes_home_override``).

Per-home memory-provider skill registries are exercised for real.
"""

from __future__ import annotations

import contextlib
from pathlib import Path

import pytest

from agent.secret_scope import build_profile_secret_scope, reset_secret_scope, set_secret_scope
from hermes_constants import reset_hermes_home_override, set_hermes_home_override

@pytest.fixture
def homes(tmp_path, monkeypatch):
    """Profile A (launch home, ``HERMES_HOME``) and profile B with different config values."""
    root = tmp_path / ".hermes"
    a, b = root, root / "profiles" / "B"
    for home, tag in ((a, "A"), (b, "B")):
        home.mkdir(parents=True)
        (home / "config.yaml").write_text(f"memory:\n  provider: prov{tag}\n", encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(a))
    return a, b


@contextlib.contextmanager
def scoped(home: Path):
    t_home = set_hermes_home_override(str(home))
    t_secret = set_secret_scope(build_profile_secret_scope(home))
    try:
        yield
    finally:
        reset_secret_scope(t_secret)
        reset_hermes_home_override(t_home)


def test_memory_provider_skill_prune_only_touches_the_active_home(homes, monkeypatch):
    """Pruning under profile B (whose active provider differs) must leave profile A's registered
    provider skill in place; A's own later prune still retracts it."""
    import plugins.memory as mem
    from hermes_cli.plugins import _reset_plugin_managers_for_tests, get_plugin_manager

    a, b = homes
    _reset_plugin_managers_for_tests()
    mem._REGISTERED_MEMORY_PROVIDER_SKILLS.clear()
    skill_dir = a / "plugins" / "provA" / "skills" / "maint"
    skill_dir.mkdir(parents=True)
    (skill_dir / "SKILL.md").write_text("---\nname: maint\ndescription: x\n---\nbody\n", encoding="utf-8")
    try:
        with scoped(a):
            mem._ProviderCollector("provA").register_skill("maint", skill_dir)
            assert get_plugin_manager().find_plugin_skill("provA:maint") is not None
        with scoped(b):
            mem._prune_inactive_memory_provider_skills("provB")
        with scoped(a):
            assert get_plugin_manager().find_plugin_skill("provA:maint") is not None
            mem._prune_inactive_memory_provider_skills("provOther")
            assert get_plugin_manager().find_plugin_skill("provA:maint") is None
    finally:
        mem._REGISTERED_MEMORY_PROVIDER_SKILLS.clear()
        _reset_plugin_managers_for_tests()
