"""The agent's own ``SOUL.self.md`` rides the frozen plugin-section slot (lina#359)."""
from __future__ import annotations

from unittest.mock import patch

import pytest

import agent.background_review as bg_review
import run_agent
from agent.conversation_loop import _restore_or_build_system_prompt
from agent.self_note_section import SELF_NOTE_BOUNDARY
from agent.system_prompt import build_system_prompt, invalidate_system_prompt
from hermes_cli import plugins
from hermes_cli.plugins import (
    MAX_SYSTEM_PROMPT_SECTION_CHARS, PLUGIN_SECTIONS_END, PluginContext, PluginManager, PluginManifest,
)
from hermes_constants import reset_hermes_home_override, set_hermes_home_override
from hermes_state import SessionDB
from run_agent import AIAgent
from tests.agent.test_background_review_cache_parity import _make_recorder_class

FRAME = "## Plugin Context: agent-self-note"
NOTE_A = "Voice: short sentences.\n- [2026-11-14] likes plain tables"
NOTE_B = "Voice: short sentences.\n- [2026-11-15] likes plain lists"


@pytest.fixture
def home(tmp_path, monkeypatch):
    # The workspace block shells out to git on every build; pin it so builds differ only by what the test changes.
    monkeypatch.setattr("agent.coding_context.build_coding_workspace_block",
                        lambda cwd=None: "Workspace (snapshot at session start):\n- Root: /pinned")
    manager = PluginManager()
    manager._discovered = True
    monkeypatch.setattr(plugins, "_plugin_manager", manager)
    path = tmp_path / "home"
    path.mkdir()
    token = set_hermes_home_override(path)
    yield path
    reset_hermes_home_override(token)


def _agent(session_id: str, **over) -> AIAgent:
    kwargs = dict(api_key="test-key", base_url="https://openrouter.ai/api/v1", model="test/model",
                  provider="openrouter", platform="cli", quiet_mode=True, skip_context_files=True,
                  skip_memory=True, session_id=session_id)
    kwargs.update(over)
    return AIAgent(**kwargs)


def _write_note(home, text: str) -> None:
    (home / "SOUL.self.md").write_text(text, encoding="utf-8")


def _section(prompt: str) -> str:
    return prompt[prompt.index(FRAME):prompt.index(PLUGIN_SECTIONS_END)]


def test_two_new_sessions_on_the_same_day_with_an_unchanged_note_are_byte_identical(home):
    _write_note(home, NOTE_A)
    first = build_system_prompt(_agent("20261115_090000_aaaaaa"))
    second = build_system_prompt(_agent("20261115_180000_bbbbbb"))

    section = _section(first)
    assert section.index(SELF_NOTE_BOUNDARY) < section.index(NOTE_A)
    assert first.index(FRAME) < first.index("Conversation started:")
    assert first == second


def test_a_changed_note_moves_the_first_differing_byte_into_its_section(home):
    _write_note(home, NOTE_A)
    before = build_system_prompt(_agent("20261115_090000_aaaaaa"))
    _write_note(home, NOTE_B)
    after = build_system_prompt(_agent("20261115_180000_bbbbbb"))

    first_diff = next(i for i, (x, y) in enumerate(zip(before, after)) if x != y)
    assert before.index(FRAME) < first_diff < before.index(PLUGIN_SECTIONS_END)
    assert NOTE_B in after and NOTE_A not in after


def test_a_mid_session_edit_reaches_the_prompt_only_at_the_compaction_rebuild(home):
    _write_note(home, NOTE_A)
    agent = _agent("20261115_090000_aaaaaa")
    first = build_system_prompt(agent)
    _write_note(home, NOTE_B)

    assert build_system_prompt(agent) == first
    invalidate_system_prompt(agent)
    rebuilt = build_system_prompt(agent)
    start = first.index(FRAME)
    assert NOTE_B in _section(rebuilt) and NOTE_A not in rebuilt
    assert rebuilt[:start] == first[:start]


def test_a_resumed_session_keeps_the_stored_section_bytes_not_the_file(home):
    _write_note(home, NOTE_A)
    db = SessionDB(db_path=home / "state.db")
    session_id = "20261115_090000_aaaaaa"
    db.ensure_session(session_id, source="cli", model="test/model")
    try:
        first = _agent(session_id, session_db=db)
        _restore_or_build_system_prompt(first, None, [])
        _write_note(home, NOTE_B)

        resumed = _agent(session_id, session_db=db)
        _restore_or_build_system_prompt(resumed, None, [{"role": "user", "content": "already persisted"}])
    finally:
        db.close()

    assert resumed._cached_system_prompt == first._cached_system_prompt
    assert NOTE_A in resumed._cached_system_prompt and NOTE_B not in resumed._cached_system_prompt


def test_each_profile_reads_its_own_home_note(home, tmp_path, monkeypatch):
    other = tmp_path / "other"
    other.mkdir()
    _write_note(home, NOTE_A)
    _write_note(other, NOTE_B)

    token = set_hermes_home_override(other)
    try:
        bound = build_system_prompt(_agent("20261115_090000_aaaaaa"))
    finally:
        reset_hermes_home_override(token)
    assert NOTE_B in bound and NOTE_A not in bound

    # A thread that lost the home ContextVar: the agent's own state.db names its home, not the ambient one.
    monkeypatch.setenv("HERMES_HOME", str(home))
    db = SessionDB(db_path=other / "state.db")
    token = set_hermes_home_override(None)
    try:
        unbound = build_system_prompt(_agent("20261115_090000_bbbbbb", session_db=db))
    finally:
        reset_hermes_home_override(token)
        db.close()
    assert NOTE_B in unbound and NOTE_A not in unbound


def _register_plugin_section():
    ctx = PluginContext(PluginManifest(name="example-plugin", key="example-plugin", source="user"),
                        plugins._plugin_manager)
    ctx.register_system_prompt_section("example.rules", "plugin rules", position="after_memory")


def test_cron_sessions_carry_the_note_like_plugin_sections_and_safe_workers_carry_neither(home):
    _write_note(home, NOTE_A)
    _register_plugin_section()

    cron = build_system_prompt(_agent("20261115_090000_aaaaaa", platform="cron"))
    assert NOTE_A in cron and "## Plugin Context: example.rules" in cron

    with patch("agent.safe_worker_policy.safe_worker_enabled", return_value=True):
        safe = build_system_prompt(_agent("20261115_090000_bbbbbb"))
    assert NOTE_A not in safe and "## Plugin Context:" not in safe


def test_a_background_review_fork_inherits_the_parents_note_bytes(home):
    _write_note(home, NOTE_A)
    parent = _agent("20261115_090000_aaaaaa")
    parent._cached_system_prompt = build_system_prompt(parent)
    _write_note(home, NOTE_B)
    runtime = {"provider": parent.provider, "model": parent.model, "api_key": "test-key", "base_url": None,
               "api_mode": None, "credential_pool": None, "request_overrides": {}, "max_tokens": None,
               "command": None, "args": [], "routed": False}

    with patch.object(run_agent, "AIAgent", _make_recorder_class()), \
            patch.object(bg_review, "_resolve_review_runtime", return_value=runtime):
        fork, _rt, routed = bg_review.build_cache_parity_fork(parent, None, max_iterations=5)

    assert not routed
    assert fork._cached_system_prompt == parent._cached_system_prompt
    assert NOTE_A in fork._cached_system_prompt and NOTE_B not in fork._cached_system_prompt


@pytest.mark.parametrize("text", [None, "", "  \n\n "])
def test_a_missing_or_empty_note_adds_no_bytes(home, text):
    baseline = build_system_prompt(_agent("20261115_090000_aaaaaa"))
    if text is not None:
        _write_note(home, text)
    prompt = build_system_prompt(_agent("20261115_090000_aaaaaa"))
    assert prompt == baseline and FRAME not in prompt


def test_an_oversized_note_is_truncated_and_still_frozen(home):
    _write_note(home, "# Head\n" + "head line\n" * 300 + "# Middle\n" + "x" * 6000 + "\n# Tail\nlast line")
    agent = _agent("20261115_090000_aaaaaa")
    prompt = build_system_prompt(agent)

    content = agent._plugin_system_prompt_sections_snapshot[0].content
    assert len(content) <= MAX_SYSTEM_PROMPT_SECTION_CHARS
    assert content.startswith(SELF_NOTE_BOUNDARY) and "head line" in content and "last line" in content
    assert "[...truncated SOUL.self.md" in content and content in prompt


def test_a_long_marker_still_leaves_the_notes_tail_inside_the_section_limit(home):
    # Given an oversized note whose omitted middle holds many long headings, so the marker is large
    headings = "".join(f"## {i:02d} " + "h" * 185 + "\nbody line\n" for i in range(15))
    _write_note(home, "# Head\n" + "head line\n" * 300 + headings + "x" * 4000 + "\n# Tail\nfinal line one\nfinal line two")
    agent = _agent("20261115_090000_aaaaaa")
    build_system_prompt(agent)

    content = agent._plugin_system_prompt_sections_snapshot[0].content
    # Then the section fits, the marker is whole, and the last lines of the note survive
    assert len(content) <= MAX_SYSTEM_PROMPT_SECTION_CHARS
    assert "If you need the full instructions" in content
    assert content.rstrip().endswith("final line two")


def test_the_frozen_sections_are_recovered_from_a_real_persisted_prompt(home):
    from agent.system_prompt import _restore_plugin_prompt_sections
    _write_note(home, NOTE_A)
    prompt = build_system_prompt(_agent("20261115_090000_cccccc"))

    restored = _restore_plugin_prompt_sections(prompt)

    assert [section.id for section in restored] == ["agent-self-note"]
    assert NOTE_A in restored[0].content
