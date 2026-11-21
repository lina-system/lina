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
