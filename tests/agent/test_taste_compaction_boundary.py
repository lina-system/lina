"""A late compaction may refresh owner tastes only during the admitted turn."""

from contextlib import closing
from contextvars import Context, copy_context
from pathlib import Path
from unittest.mock import patch

import pytest

from agent import conversation_compression as compression
from agent.memory_manager import MemoryManager
from agent.self_note_section import SELF_NOTE_FILENAME, SELF_NOTE_SECTION_ID
from agent.system_prompt import _restore_plugin_prompt_sections
from agent.turn_context_taste import owner_taste_prompt_scope
from hermes_constants import reset_hermes_home_override, set_hermes_home_override
from plugins.memory.lina import LinaMemoryProvider
from plugins.memory.lina.policy import load_policy
from run_agent import AIAgent
from tests.agent.test_413_compression import _make_413_error, _mock_response


def _note(prompt: str) -> str:
    return next(s.content for s in _restore_plugin_prompt_sections(prompt) if s.id == SELF_NOTE_SECTION_ID)


class TurnEnded(Exception):
    """The scripted tool round finished. Stop before the first model call."""


@pytest.mark.parametrize("owner", [True, False], ids=["owner", "nonowner"])
@pytest.mark.parametrize("trigger", ["post_tool", "overflow"])
def test_late_compaction_refreshes_only_the_live_owners_profile(tmp_path: Path, monkeypatch, owner, trigger):
    home = tmp_path / "owner"
    home.mkdir()
    other = tmp_path / "other"
    other.mkdir()
    note = home / SELF_NOTE_FILENAME
    note.write_text("Original voice.", encoding="utf-8")
    (other / SELF_NOTE_FILENAME).write_text("Other voice.", encoding="utf-8")
    tastes = {home: ("- Etching.",), other: ("- Pastels.",)}
    monkeypatch.setattr("hermes_cli.heart.taste_context.core_lines", tastes.__getitem__)
    monkeypatch.setattr("hermes_cli.heart.taste_context.pick_lines", lambda home, query: ())

    token = set_hermes_home_override(home)
    try:
        with closing(AIAgent(
            api_key="test", base_url="http://127.0.0.1:1/v1", model="test/model", provider="custom",
            platform="cli", quiet_mode=True, skip_memory=True, skip_context_files=True,
            skip_background_review=True, enabled_toolsets=["todo"], save_trajectories=False,
        )) as agent:
            manager = MemoryManager()
            manager.initialize_all(agent.session_id, hermes_home=home)
            provider = LinaMemoryProvider.__new__(LinaMemoryProvider)
            provider._policy, provider._init = load_policy(), {"platform": "cli"}
            monkeypatch.setattr(manager, "get_provider", lambda name: provider if name == "lina-memory" else None)
            monkeypatch.setattr(agent, "_memory_manager", manager)
            with owner_taste_prompt_scope(agent, home):
                agent._cached_system_prompt = agent._build_system_prompt()
            frozen = _note(agent._cached_system_prompt)
            assert "- Etching." in frozen and "Original voice." in frozen

            worker_contexts: list[Context] = []

            def compact(live_agent, messages, system_message, **kwargs):
                assert kwargs["trigger"] == trigger
                worker_contexts.append(copy_context())
                # Compaction must use the home admitted at turn entry even if ambient lookup changes.
                worker_token = set_hermes_home_override(other)
                try:
                    return messages[-1:], compression._rebuild_system_prompt_at_boundary(live_agent, system_message)
                finally:
                    reset_hermes_home_override(worker_token)

            def edit_profile():
                note.write_text("Revised voice.", encoding="utf-8")
                tastes[home] = ("- Woodcuts.",) if trigger == "post_tool" else ()

            def tool_round():
                edit_profile()
                agent.compression_enabled = True
                monkeypatch.setattr(agent.context_compressor, "should_compress", lambda tokens: True)
                yield "", "tool_describe", {"names": ["todo_list"]}
                raise TurnEnded

            def reply(api_kwargs, *, on_first_delta):
                if not worker_contexts:
                    edit_profile()
                    raise _make_413_error()
                return _mock_response(content="Recovered.")

            author = None if owner else {"id": "bot:test-relay", "is_bot": True}
            with (
                patch.object(compression, "compress_context", compact),
                patch.object(compression, "_refresh_agent_tool_definitions", return_value=False),
                patch.object(agent, "_session_db", None),
            ):
                if trigger == "post_tool":
                    with pytest.raises(TurnEnded):
                        agent.run_conversation("Check tasks.", prelude=tool_round(), turn_author=author)
                else:
                    history = [{"role": "user", "content": "Earlier."}, {"role": "assistant", "content": "Done."}]
                    with patch.object(agent, "_interruptible_streaming_api_call", side_effect=reply):
                        result = agent.run_conversation("Check tasks.", conversation_history=history, turn_author=author)
                    assert result["final_response"] == "Recovered."

            worker_context, = worker_contexts
            refreshed = _note(agent._cached_system_prompt)
            if owner:
                assert "Revised voice." in refreshed
                if trigger == "post_tool":
                    assert "- Woodcuts." in refreshed
                assert "- Etching." not in refreshed and "Original voice." not in refreshed
                assert "- Pastels." not in refreshed and "Other voice." not in refreshed
            else:
                assert refreshed == frozen

            tastes[home] = ("- Watercolors.",)
            # A worker can outlive its caller and keep a copy of the caller's ContextVars.
            with patch.object(compression, "_refresh_agent_tool_definitions", return_value=False):
                late = worker_context.run(compression._rebuild_system_prompt_at_boundary, agent, "")
            assert _note(late) == refreshed
    finally:
        reset_hermes_home_override(token)
