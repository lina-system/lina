"""Contracts for the owner's frozen rules and their next-turn difference."""

from types import SimpleNamespace
from unittest.mock import patch


def test_first_rule_notice_requires_a_known_frozen_snapshot(monkeypatch):
    from agent import standing_rules_section as rules
    from agent.system_prompt import restore_plugin_prompt_sections

    line = "- all (2026-01-01): Show both units."
    monkeypatch.setattr(rules, "read_rule_lines", lambda agent: (line,))
    agent = SimpleNamespace(_cached_system_prompt="You are helpful.")

    assert not rules.standing_rules_note(agent, [])

    restore_plugin_prompt_sections(agent, agent._cached_system_prompt)
    notice = rules.standing_rules_note(agent, [])
    assert line in notice
    assert not rules.standing_rules_note(agent, [{"role": "user", "api_content": notice}])


def test_overflow_preserves_every_complete_rule_on_the_user_turn(monkeypatch):
    from agent import standing_rules_section as rules

    lines = tuple(f"- all (2026-01-01): Rule {i:02}. " + "Use a short sentence. " * 4 for i in range(40))
    monkeypatch.setattr(rules, "read_rule_lines", lambda agent: lines)
    agent = SimpleNamespace()
    section, = rules.render_standing_rules_section(agent)
    assert len(section.content) <= 4000
    assert "40" in section.content and "user" in section.content
    agent._plugin_system_prompt_sections_snapshot = (section,)
    notice = rules.standing_rules_note(agent, [])
    assert all(line in notice for line in lines)
    assert not rules.standing_rules_note(agent, [{"role": "user", "api_content": notice}])


def test_new_agent_announces_full_frozen_difference_only_when_it_changes(monkeypatch):
    from agent import standing_rules_section as rules

    old, new, another = ("- agent (2026-01-01): " + text for text in ("Use bullets.", "Use prose.", "Sign off."))
    lines = [old]
    monkeypatch.setattr(rules, "read_rule_lines", lambda agent: tuple(lines))
    frozen = rules.render_standing_rules_section(SimpleNamespace())

    def current(history):
        return rules.standing_rules_note(SimpleNamespace(_plugin_system_prompt_sections_snapshot=frozen), history)

    lines[:] = [new]
    first = current([])
    assert old in first and new in first
    history = [{"role": "user", "content": "Proceed.", "api_content": first}]
    assert not current(history)
    lines.append(another)
    second = current(history)
    assert all(line in second for line in (old, new, another))
    history.append({"role": "user", "api_content": second})
    lines[:] = [old]
    restored = current(history)
    assert "accurate again" in restored
    assert not current([*history, {"role": "user", "api_content": restored}])


def test_persisted_rules_survive_resume_and_failed_read_until_successful_compaction(tmp_path, monkeypatch):
    from agent import standing_rules_section as rules
    from agent.conversation_loop import _restore_or_build_system_prompt
    from agent.system_prompt import build_system_prompt, invalidate_system_prompt
    from hermes_state import SessionDB
    from tests.agent.test_self_note_prompt_section import _agent

    sid = "20260101_090000_aaaaaa"
    old, new = "- all (2025-12-30): Use metric units.", "- all (2026-01-01): Show both units."
    with SessionDB(db_path=tmp_path / "state.db") as db:
        db.ensure_session(sid, source="cli", model="test/model")
        first = _agent(sid, session_db=db)
        monkeypatch.setattr(rules, "read_rule_lines", lambda agent: (old,))
        _restore_or_build_system_prompt(first, None, [])
        assert old in first._cached_system_prompt

        resumed = _agent(sid, session_db=db)
        with patch.object(rules, "read_rule_lines", side_effect=OSError("store offline")) as reader:
            _restore_or_build_system_prompt(resumed, None, [{"role": "user", "content": "Resume."}])
            assert resumed._cached_system_prompt == first._cached_system_prompt
            reader.assert_not_called()
            invalidate_system_prompt(resumed)
            rebuilt = build_system_prompt(resumed)
            assert old in rebuilt
            assert resumed._plugin_system_prompt_sections_snapshot[0].content == (
                first._plugin_system_prompt_sections_snapshot[0].content
            )
        monkeypatch.setattr(rules, "read_rule_lines", lambda agent: (new,))
        invalidate_system_prompt(resumed)
        refreshed = build_system_prompt(resumed)
        assert new in refreshed and old not in refreshed


def test_session_store_without_goal_registry_or_message_store_reads_rules_from_own_handle(monkeypatch, tmp_path):
    """A managed worker's RuntimeSessionStore has no get_meta or message_store (see the surface parity test)."""
    import hermes_state_store
    from agent import standing_rules_section as rules

    seen = {}

    class Reader:
        def __init__(self, agent_id):
            seen["agent_id"] = agent_id

        def standing_rules(self, session_id, gateway_key, places):
            seen["places"] = places
            return [("all", None, "2026-01-01", "Show both units.")]

        def close(self):
            seen["closed"] = True

    monkeypatch.setattr(hermes_state_store, "MessageStore", Reader)
    agent = SimpleNamespace(_session_db=object(), session_id="s1", _gateway_session_key="g1", _agent_home=tmp_path)
    assert rules.read_rule_lines(agent) == ("- all (2026-01-01): Show both units.",)
    assert seen["places"] == ["channel:g1"] and seen["closed"]


def test_modes_without_the_api_content_sidecar_never_get_a_rules_notice(monkeypatch):
    # MoA and codex_app_server turns never stamp api_content, so a notice sent once could not be read
    # back from the transcript and would be announced again on every later turn.
    from agent import standing_rules_section as rules
    from agent.system_prompt import restore_plugin_prompt_sections

    line = "- all (2026-01-01): Show both units."
    monkeypatch.setattr(rules, "read_rule_lines", lambda agent: (line,))
    for extra in ({"provider": "moa"}, {"api_mode": "codex_app_server"}):
        agent = SimpleNamespace(_cached_system_prompt="You are helpful.", **extra)
        restore_plugin_prompt_sections(agent, agent._cached_system_prompt)
        assert rules.standing_rules_note(agent, []) == ""
    agent = SimpleNamespace(_cached_system_prompt="You are helpful.", provider="anthropic", api_mode="chat_completions")
    restore_plugin_prompt_sections(agent, agent._cached_system_prompt)
    assert line in rules.standing_rules_note(agent, [])


def test_oversized_rules_in_modes_without_a_notice_keep_whole_rules_and_name_the_tool(monkeypatch):
    from agent import standing_rules_section as rules

    lines = tuple(f"- all (2026-01-01): Rule {i:02}. " + "Use a short sentence. " * 4 for i in range(40))
    monkeypatch.setattr(rules, "read_rule_lines", lambda agent: lines)
    for extra in ({"provider": "moa"}, {"api_mode": "codex_app_server"}):
        section, = rules.render_standing_rules_section(SimpleNamespace(**extra))
        text = section.content
        assert len(text) <= 4000
        assert "next user message" not in text and "standing_rule tool" in text
        shown = [line for line in lines if line in text]
        assert shown and len(shown) < len(lines) and list(lines[:len(shown)]) == shown
        assert f"{len(shown)} of 40 lines shown" in text


def test_each_notice_supersedes_an_earlier_update_for_a_rule_that_was_never_frozen(monkeypatch):
    from agent import standing_rules_section as rules

    first_rule, second_rule = ("- agent (2026-01-01): " + text for text in ("Use bullets.", "Use prose."))
    lines = []
    monkeypatch.setattr(rules, "read_rule_lines", lambda agent: tuple(lines))
    frozen = (rules.RenderedPluginSystemPromptSection(
        id=rules.SECTION_ID, content=rules.BOUNDARY, position="after_memory", plugin="core"),)
    agent = SimpleNamespace(_plugin_system_prompt_sections_snapshot=frozen)

    lines[:] = [first_rule]
    first = rules.standing_rules_note(agent, [])
    history = [{"role": "user", "api_content": first}]
    lines[:] = [second_rule]
    second = rules.standing_rules_note(agent, history)
    # The first rule is in no frozen section, so the second notice cannot list it as ended. It must say
    # that it replaces every earlier update still sitting in the replay history.
    assert second_rule in second and first_rule not in second
    assert "replaces every earlier standing rules update" in second


def test_notice_kept_through_a_rebuild_is_retracted_when_the_rules_equal_the_new_baseline(monkeypatch):
    # Rule B is announced after the freeze and then ended; compaction rebuilds the frozen section (new frame,
    # so new hash) but keeps the user row that carried the B notice. The rebuilt section equals the current
    # rules, yet the retained notice still tells the model to apply B.
    from agent import standing_rules_section as rules

    rule_b = "- agent (2026-01-01): Use bullets."
    lines = []
    monkeypatch.setattr(rules, "read_rule_lines", lambda agent: tuple(lines))
    old_frame = (rules.RenderedPluginSystemPromptSection(
        id=rules.SECTION_ID, content=rules.BOUNDARY, position="after_memory", plugin="core"),)
    lines[:] = [rule_b]
    announced = rules.standing_rules_note(SimpleNamespace(_plugin_system_prompt_sections_snapshot=old_frame), [])
    assert rule_b in announced
    history = [{"role": "user", "api_content": announced}]

    sign_off = "- all (2026-01-02): Sign off."
    rebuilt = (rules.RenderedPluginSystemPromptSection(
        id=rules.SECTION_ID, content=rules.BOUNDARY + "\n" + sign_off, position="after_memory", plugin="core"),)
    lines[:] = [sign_off]
    new_agent = SimpleNamespace(_plugin_system_prompt_sections_snapshot=rebuilt)
    retraction = rules.standing_rules_note(new_agent, history)
    assert "replaces every earlier standing rules update" in retraction and rule_b not in retraction
    # Once the retraction sits in history, the next turn adds nothing.
    assert not rules.standing_rules_note(new_agent, [*history, {"role": "user", "api_content": retraction}])
