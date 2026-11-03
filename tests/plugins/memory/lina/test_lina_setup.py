"""lina#20: ``hermes memory setup`` choosing lina-memory runs Honcho's wizard and leaves lina-memory in the memory
slot, also on a home that held Honcho or the built-in memory before; a cancelled wizard leaves the slot alone."""

from __future__ import annotations

import pytest

from hermes_cli.config import cfg_get, load_config


@pytest.fixture
def wizard(tmp_path, monkeypatch):
    """A home with ``memory.provider: <before>`` and Honcho's wizard answered with its defaults, offline."""
    from plugins.memory import import_provider_module
    import hermes_cli.memory_setup as memory_setup

    cli = import_provider_module("honcho", "cli")
    monkeypatch.setattr(memory_setup, "_install_dependencies", lambda name: None)
    monkeypatch.setattr(cli, "_ensure_sdk_installed", lambda: True)
    monkeypatch.setattr(cli, "_prompt", lambda label, default=None, secret=False: default or "cloud")
    monkeypatch.setattr(cli, "_setup_identity_mapping", lambda *a, **k: None)
    monkeypatch.setattr(cli, "_setup_tuning", lambda *a, **k: None)
    monkeypatch.setattr(cli, "_connect", lambda *a, **k: (_ for _ in ()).throw(OSError("offline")))

    def make(before: str, *, signs_in: bool):
        home = tmp_path / f"home-{before or 'builtin'}"
        home.mkdir()
        (home / "config.yaml").write_text(f"memory:\n  provider: '{before}'\n", encoding="utf-8")
        monkeypatch.setenv("HERMES_HOME", str(home))
        monkeypatch.setattr(cli, "_setup_cloud_auth", lambda *a, **k: signs_in)
        return lambda: memory_setup.cmd_setup_provider("lina-memory")
    return make


@pytest.mark.parametrize("before", ["honcho", ""])
def test_setting_up_lina_memory_puts_it_in_the_slot(wizard, before):
    wizard(before, signs_in=True)()
    assert cfg_get(load_config(), "memory", "provider") == "lina-memory"


def test_a_cancelled_lina_memory_setup_leaves_the_slot(wizard):
    wizard("honcho", signs_in=False)()
    assert cfg_get(load_config(), "memory", "provider") == "honcho"
