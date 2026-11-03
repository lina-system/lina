"""lina#20: while lina-memory holds the memory slot, the Honcho adapter it drives keeps its ``hermes honcho`` CLI,
and setting Honcho up keeps lina-memory in the slot."""

from __future__ import annotations

import pytest


@pytest.fixture
def home_with(tmp_path, monkeypatch):
    def make(provider: str):
        home = tmp_path / provider
        home.mkdir()
        (home / "config.yaml").write_text(f"memory:\n  provider: {provider}\n", encoding="utf-8")
        monkeypatch.setenv("HERMES_HOME", str(home))
        return home
    return make


def test_lina_memory_offers_the_honcho_cli_and_keeps_the_slot_through_honcho_setup(home_with):
    from plugins.memory import discover_plugin_cli_commands, slot_provider_for

    home_with("lina-memory")
    commands = {cmd["name"]: cmd for cmd in discover_plugin_cli_commands()}
    assert "honcho" in commands and commands["honcho"]["handler_fn"].__name__ == "honcho_command"
    assert slot_provider_for("honcho") == "lina-memory"


def test_honcho_alone_is_set_up_into_the_slot_itself(home_with):
    from plugins.memory import slot_provider_for

    home_with("honcho")
    assert slot_provider_for("honcho") == "honcho"
