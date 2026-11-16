"""lina#467: the shared owner profile in the assembled system prompt of every agent of an install."""

import json
from types import SimpleNamespace
from unittest.mock import patch

import pytest

from agent.system_prompt import build_system_prompt
from tools.memory_tool import MemoryStore, memory_tool

NAMES = ("a", "b", "c")


@pytest.fixture()
def install(tmp_path, monkeypatch):
    root = tmp_path / "install"
    (root / "profiles").mkdir(parents=True)
    (root / "config.yaml").write_text("{}\n")
    for name in NAMES:
        (root / "profiles" / name).mkdir()
        (root / "profiles" / name / "config.yaml").write_text("{}\n")

    def use(name: str) -> MemoryStore:
        """Make *name* the active profile and start its agent's store (a new session)."""
        monkeypatch.setenv("HERMES_HOME", str(root / "profiles" / name))
        store = MemoryStore()
        store.load_from_disk()
        return store
    use.root = root
    return use


def _write(store, target, operation):
    result = json.loads(memory_tool(target=target, operations=[operation], store=store))
    assert result["success"], result


def _prompt(store) -> str:
    agent = SimpleNamespace(
        load_soul_identity=False, skip_context_files=True, valid_tool_names=[], _task_completion_guidance=False,
        _tool_use_enforcement=False, _environment_probe=False, _kanban_worker_guidance="", _memory_store=store,
        _memory_enabled=True, _user_profile_enabled=True, _memory_manager=None, model="", provider="", platform="",
        pass_session_id=False, session_id="20261116_000000_abcdef", _emit_status=lambda *_a, **_k: None)
    with patch("agent.prompt_builder.build_environment_hints", return_value=""):
        return build_system_prompt(agent)


def test_a_shared_fact_reaches_every_agent_and_is_stored_once(install):
    for name in NAMES:
        _write(install(name), "user", {"action": "add", "content": f"Agent {name} helped with the garden"})
    _write(install("a"), "shared", {"action": "add", "content": "The owner prefers short answers"})
    for name in NAMES:
        prompt = _prompt(install(name))
        assert prompt.count("The owner prefers short answers") == 1
        assert prompt.index("SHARED OWNER PROFILE") < prompt.index(f"Agent {name} helped with the garden")
    holders = [p for p in install.root.rglob("*.md") if "The owner prefers short answers" in p.read_text()]
    assert holders == [install.root / "shared" / "OWNER.md"]


def test_a_per_agent_change_keeps_the_shared_block_and_the_prefix_before_it(install):
    store = install("a")
    _write(store, "shared", {"action": "add", "content": "The owner writes in Korean"})
    _write(store, "memory", {"action": "add", "content": "The repo uses ruff"})
    _write(store, "user", {"action": "add", "content": "Asked about the garden"})
    before_store = install("a")
    before = _prompt(before_store)
    _write(before_store, "user", {"action": "patch", "old_text": "garden", "content": "kitchen"})
    _write(before_store, "memory", {"action": "add", "content": "Tests run through scripts/run_tests.sh"})
    after_store = install("a")
    after = _prompt(after_store)
    shared_block = before_store.format_for_system_prompt("shared")
    assert after_store.format_for_system_prompt("shared") == shared_block
    end_of_shared = before.index(shared_block) + len(shared_block)
    assert after[:end_of_shared] == before[:end_of_shared]
    assert after != before and "Asked about the kitchen" in after
