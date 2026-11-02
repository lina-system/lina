"""The bundled LCM-X context engine through the real ``AIAgent`` init path (lina#15).

Every test runs a home whose config.yaml says ``context.engine: lcm-x``, so ``_select_context_engine``
loads ``plugins/context_engine/lcm-x`` with the real plugin context: the engine's ``post_llm_call`` ingest
hook, ``pre_llm_call`` and subagent hooks register on the home's plugin manager. Turns run against a
stubbed provider client and the summary model is a local fake; nothing reaches the network.
"""

from __future__ import annotations

import contextlib
import json
import re
import sqlite3
import sys
import textwrap
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

import pytest

from hermes_constants import reset_hermes_home_override, set_hermes_home_override

LCM_HOOKS = ("pre_llm_call", "post_llm_call", "subagent_start", "subagent_stop")


@pytest.fixture(autouse=True)
def _clean_managers(monkeypatch):
    from hermes_cli.plugins import _reset_plugin_managers_for_tests

    # FastEmbed is not a LINA dependency; with embeddings on, LCM-X starts a retry worker for it.
    monkeypatch.setenv("LCM_EMBEDDINGS_ENABLED", "false")
    # tiktoken would fetch its BPE file over the network; without it LCM-X counts by characters.
    monkeypatch.setitem(sys.modules, "tiktoken", None)
    _reset_plugin_managers_for_tests()
    yield
    _reset_plugin_managers_for_tests()


def _home(path: Path, extra: str = "") -> Path:
    path.mkdir(parents=True, exist_ok=True)
    (path / "config.yaml").write_text("context:\n  engine: lcm-x\n" + extra, encoding="utf-8")
    return path


@pytest.fixture
def home(tmp_path, monkeypatch):
    home = _home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


@contextlib.contextmanager
def scoped(home: Path):
    token = set_hermes_home_override(str(home))
    try:
        yield
    finally:
        reset_hermes_home_override(token)


def _agent(session_id: str, engine_name: str = "lcm-x"):
    from run_agent import AIAgent

    with (
        patch("model_tools.get_tool_definitions", return_value=[]),
        patch("model_tools.check_toolset_requirements", return_value={}),
        patch("agent.process_bootstrap.OpenAI"),
    ):
        agent = AIAgent(api_key="test-key-1234567890", base_url="https://openrouter.ai/api/v1", quiet_mode=True,
                        skip_context_files=True, skip_memory=True, session_id=session_id)
    assert type(agent.context_compressor).__module__ == "plugins.context_engine.lcm-x.engine"
    assert agent.context_compressor.name == engine_name
    return agent


def _turn(agent, user: str, reply: str, history=None) -> dict:
    message = SimpleNamespace(content=reply, tool_calls=None, reasoning_content=None, reasoning=None)
    agent.client = MagicMock()
    agent.client.chat.completions.create.return_value = SimpleNamespace(
        choices=[SimpleNamespace(message=message, finish_reason="stop")], model="test/model", usage=None)
    agent._cached_system_prompt = "You are helpful."
    result = agent.run_conversation(user, conversation_history=history)
    assert result.get("final_response") == reply
    return result


def _stored(home: Path, session_id: str) -> list[tuple[str, str]]:
    with contextlib.closing(sqlite3.connect(home / "lcm.db")) as con:
        return con.execute("SELECT role, content FROM messages WHERE session_id = ? ORDER BY store_id",
                           (session_id,)).fetchall()


def _hook_counts():
    from hermes_cli.plugins import get_plugin_manager

    hooks = get_plugin_manager()._hooks
    return {name: len(hooks.get(name, [])) for name in LCM_HOOKS}


def _fake_summary_model(**kwargs):
    """Answers LCM-X's summary request inside the nonce envelope its integrity contract requires."""
    system = "\n".join(m["content"] for m in kwargs["messages"] if m["role"] == "system")
    nonce = re.search(r'<lcm-summary nonce="([0-9a-f]+)">', system).group(1)
    body = "The user asked a long series of numbered questions and the assistant answered each one. " * 3
    text = (f'<lcm-summary nonce="{nonce}">\n{body}\n'
            "Expand for details about: the numbered questions and answers\n</lcm-summary>")
    return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=text))])


def test_finished_turn_reaches_lcm_db_through_the_post_llm_call_hook(home):
    agent = _agent("sess-ingest")
    assert _hook_counts() == dict.fromkeys(LCM_HOOKS, 1)

    _turn(agent, "Remember the code word PELICAN-7731.", "Noted: PELICAN-7731.")

    assert _stored(home, "sess-ingest") == [
        ("user", "Remember the code word PELICAN-7731."), ("assistant", "Noted: PELICAN-7731.")]


def test_second_agent_on_the_session_reads_back_what_the_first_stored(home):
    _turn(_agent("sess-resume"), "Remember the code word PELICAN-7731.", "Noted: PELICAN-7731.")

    resumed = _agent("sess-resume")
    found = resumed.context_compressor.handle_tool_call("lcm_grep", {"query": "PELICAN"}, messages=[])

    assert '"total_results": 2' in found
    assert "Noted: >>>PELICAN<<<-7731." in found


def test_compress_summarises_the_stored_turns_and_shrinks_the_messages(home):
    agent = _agent("sess-compact")
    history: list[dict] = []
    for i in range(30):
        result = _turn(agent, f"question {i} " + "lorem ipsum dolor sit amet " * 40,
                       f"answer {i} " + "consectetur adipiscing elit" * 40, history)
        history = result["messages"]
    assert len(_stored(home, "sess-compact")) == 60  # stored turn by turn by the hook

    engine = agent.context_compressor
    messages = [{"role": "system", "content": "You are helpful."}, *history]
    with patch("agent.auxiliary_client.call_llm", side_effect=_fake_summary_model):
        compacted = engine.compress(messages, current_tokens=engine.context_length, force=True)

    assert len(compacted) < len(messages)
    assert any("Expand for details about" in str(m.get("content")) for m in compacted)
    with contextlib.closing(sqlite3.connect(home / "lcm.db")) as con:
        (nodes,) = con.execute("SELECT COUNT(*) FROM summary_nodes WHERE session_id = 'sess-compact'").fetchone()
    assert nodes >= 1
    assert len(_stored(home, "sess-compact")) == 60  # compaction did not store the turns again


def test_two_profiles_and_two_agents_keep_their_own_turns(tmp_path, monkeypatch):
    home_a = _home(tmp_path / ".hermes")
    home_b = _home(tmp_path / ".hermes" / "profiles" / "b")
    monkeypatch.setenv("HERMES_HOME", str(home_a))

    with scoped(home_a):
        first, second = _agent("a-1"), _agent("a-2")
        for _ in range(3):  # however many agents a home builds, it holds one set of LCM-X hooks
            _agent("a-extra")
        assert _hook_counts() == dict.fromkeys(LCM_HOOKS, 1)
        _turn(first, "first agent asks", "first agent answers")
        _turn(second, "second agent asks", "second agent answers")
    with scoped(home_b):
        other = _agent("b-1")
        assert _hook_counts() == dict.fromkeys(LCM_HOOKS, 1)
        _turn(other, "profile b asks", "profile b answers")

    assert _stored(home_a, "a-1") == [("user", "first agent asks"), ("assistant", "first agent answers")]
    assert _stored(home_a, "a-2") == [("user", "second agent asks"), ("assistant", "second agent answers")]
    assert _stored(home_a, "b-1") == []
    assert _stored(home_b, "b-1") == [("user", "profile b asks"), ("assistant", "profile b answers")]
    assert _stored(home_b, "a-1") == [] and _stored(home_b, "a-2") == []


@pytest.mark.parametrize("allow_removed", [False, True], ids=["catalog-copy", "installed-past-kill-list"])
@pytest.mark.parametrize("copy_name", ["hermes-lcm-x", "hermes-lcm"])
def test_user_dir_copy_does_not_load_next_to_the_bundled_engine(tmp_path, monkeypatch, copy_name, allow_removed):
    home = _home(tmp_path / ".hermes", f"plugins:\n  enabled: [{copy_name}]\n")
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("HERMES_BUNDLED_PLUGINS", str(tmp_path / "no-bundled-plugins"))
    copy = home / "plugins" / copy_name
    copy.mkdir(parents=True)
    (copy / "plugin.yaml").write_text(f"name: {copy_name}\nversion: 0.29.1\n", encoding="utf-8")
    (copy / "__init__.py").write_text(textwrap.dedent(f"""\
        from pathlib import Path

        def register(ctx):
            Path({str(copy / "registered")!r}).write_text("loaded", encoding="utf-8")
            ctx.register_hook("post_llm_call", lambda **kwargs: None)
    """), encoding="utf-8")
    if allow_removed:  # `hermes plugins install --allow-removed` exempts a copy from the catalog kill list
        (home / "plugins" / ".install-metadata.json").write_text(json.dumps({copy_name: {
            "source": "https://github.com/electricsheephq/lcm-x", "allow_removed": True}}), encoding="utf-8")

    from hermes_cli.plugins import discover_plugins, get_plugin_manager

    discover_plugins(force=True)
    agent = _agent("sess-copy")
    _turn(agent, "hello", "hi")

    loaded = get_plugin_manager()._plugins[copy_name]
    assert not loaded.enabled and "plugins/context_engine/lcm-x" in (loaded.error or "")
    assert not (copy / "registered").exists()
    assert _hook_counts() == dict.fromkeys(LCM_HOOKS, 1)
    assert _stored(home, "sess-copy") == [("user", "hello"), ("assistant", "hi")]


def _plugin_surface():
    from hermes_cli.plugins import get_plugin_manager

    manager = get_plugin_manager()
    return {**_hook_counts(), "skill": manager.find_plugin_skill("hermes-lcm-x:hermes-lcm") is not None,
            "prompt_section": "lcm-x" in manager._system_prompt_sections}


def test_forced_plugin_discovery_keeps_the_engine_hooks_of_a_cached_agent(home, tmp_path, monkeypatch):
    """A plugin install/enable in a running gateway runs ``discover_plugins(force=True)``, whose unload-all
    disposed the engine's group; a cached agent never reruns the engine loader."""
    from hermes_cli.plugins import discover_plugins

    monkeypatch.setenv("HERMES_BUNDLED_PLUGINS", str(tmp_path / "no-bundled-plugins"))
    agent = _agent("sess-cached")
    one_set = {**dict.fromkeys(LCM_HOOKS, 1), "skill": True, "prompt_section": True}
    assert _plugin_surface() == one_set

    discover_plugins(force=True)
    assert _plugin_surface() == one_set
    _turn(agent, "asked after the rescan", "answered after the rescan")
    assert _stored(home, "sess-cached") == [
        ("user", "asked after the rescan"), ("assistant", "answered after the rescan")]

    _agent("sess-built-after")  # a later build replaces the group, never adds a second one
    discover_plugins(force=True)
    assert _plugin_surface() == one_set


@pytest.mark.parametrize("enabled", ["hermes-lcm-x", "hermes-lcm", None], ids=["copy-x", "copy-legacy", "no-plugins"])
def test_legacy_engine_name_lcm_runs_the_bundled_engine(tmp_path, monkeypatch, enabled):
    """``context.engine: lcm`` (the pre-rename name) is how homes moving to LINA are configured (lina#41)."""
    extra = f"plugins:\n  enabled: [{enabled}]\n" if enabled else ""
    home = tmp_path / ".hermes"
    home.mkdir()
    (home / "config.yaml").write_text("context:\n  engine: lcm\n" + extra, encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("HERMES_BUNDLED_PLUGINS", str(tmp_path / "no-bundled-plugins"))
    if enabled:
        copy = home / "plugins" / enabled
        copy.mkdir(parents=True)
        (copy / "plugin.yaml").write_text(f"name: {enabled}\nversion: 0.29.1\n", encoding="utf-8")
        (copy / "__init__.py").write_text(
            f"from pathlib import Path\n\ndef register(ctx):\n"
            f"    Path({str(copy / 'registered')!r}).write_text('loaded', encoding='utf-8')\n", encoding="utf-8")

    from hermes_cli.plugins import discover_plugins

    discover_plugins(force=True)
    # LCM-X answers to the legacy name while the config still uses it (plugin_identity.py), and warns.
    agent = _agent("sess-legacy", engine_name="lcm")
    assert agent.context_compressor.identity_migration["legacy_engine_alias_active"] is True
    _turn(agent, "legacy config asks", "bundled engine answers")
    assert _stored(home, "sess-legacy") == [("user", "legacy config asks"), ("assistant", "bundled engine answers")]
    if enabled:
        assert not (home / "plugins" / enabled / "registered").exists()


def _probe_cli():
    from hermes_cli.plugins_cmd_toggle import _discover_context_engines

    return [name for name, _ in _discover_context_engines()]


def _probe_engines():
    from plugins.context_engine import discover_context_engines

    return [name for name, _, available in discover_context_engines() if available]


PROBES = pytest.mark.parametrize("probe", [_probe_engines, _probe_cli], ids=["discover", "cli-dashboard"])
NO_SURFACE = {**dict.fromkeys(LCM_HOOKS, 0), "skill": False, "prompt_section": False}


@pytest.fixture
def compressor_home(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    (home / "config.yaml").write_text("context:\n  engine: compressor\n", encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("HERMES_BUNDLED_PLUGINS", str(tmp_path / "no-bundled-plugins"))
    return home


@PROBES
def test_listing_engines_registers_nothing_of_lcm_x(compressor_home, probe):
    """The CLI picker and the dashboard Plugins Hub list engines; only the selected engine registers."""
    assert "lcm-x" in probe()
    assert _plugin_surface() == NO_SURFACE


@PROBES
def test_a_compressor_turn_after_listing_engines_stays_out_of_lcm_db(compressor_home, probe):
    from run_agent import AIAgent

    probe()
    with (
        patch("model_tools.get_tool_definitions", return_value=[]),
        patch("model_tools.check_toolset_requirements", return_value={}),
        patch("agent.process_bootstrap.OpenAI"),
    ):
        agent = AIAgent(api_key="test-key-1234567890", base_url="https://openrouter.ai/api/v1", quiet_mode=True,
                        skip_context_files=True, skip_memory=True, session_id="sess-compressor")
    assert type(agent.context_compressor).__module__ == "agent.context_compressor"
    _turn(agent, "a compressor-managed question", "a compressor-managed answer")
    assert not (compressor_home / "lcm.db").exists() or _stored(compressor_home, "sess-compressor") == []


@PROBES
def test_forced_discovery_after_listing_engines_registers_nothing_of_lcm_x(compressor_home, probe):
    from hermes_cli.plugins import discover_plugins

    probe()
    discover_plugins(force=True)
    assert _plugin_surface() == NO_SURFACE


@PROBES
def test_listing_engines_next_to_a_built_lcm_x_agent_keeps_one_set(home, tmp_path, monkeypatch, probe):
    monkeypatch.setenv("HERMES_BUNDLED_PLUGINS", str(tmp_path / "no-bundled-plugins"))
    from hermes_cli.plugins import get_plugin_manager

    agent = _agent("sess-listed")
    one_set = {**dict.fromkeys(LCM_HOOKS, 1), "skill": True, "prompt_section": True}
    built = {name: list(get_plugin_manager()._hooks[name]) for name in LCM_HOOKS}
    probe()
    assert _plugin_surface() == one_set
    # The probe neither adds a set nor rebuilds the agent's: the registrations are the build's own.
    assert {name: list(get_plugin_manager()._hooks[name]) for name in LCM_HOOKS} == built
    _turn(agent, "asked after the listing", "answered after the listing")
    assert _stored(home, "sess-listed") == [("user", "asked after the listing"), ("assistant", "answered after the listing")]
