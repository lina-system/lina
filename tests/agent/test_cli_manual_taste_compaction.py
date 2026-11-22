"""The local /compress command admits its own caller at the prompt rebuild."""

from contextlib import closing, nullcontext

import pytest

from agent.memory_manager import MemoryManager
from agent.self_note_section import SELF_NOTE_FILENAME
from agent.turn_context_taste import owner_taste_home, owner_taste_prompt_scope
from cli import HermesCLI
from hermes_state import SessionDB
from plugins.memory.lina import LinaMemoryProvider
from plugins.memory.lina.policy import load_policy
from tests.agent.test_413_compression import _mock_response
from tests.agent.test_conversation_compression_manual import _exchanges
from tests.agent.test_self_note_prompt_section import FRAME, _agent, home as profile_home


@pytest.mark.parametrize("owner", [True, False], ids=["owner", "nonowner"])
def test_cli_compress_admits_the_command_independently(profile_home, monkeypatch, owner):
    (profile_home / SELF_NOTE_FILENAME).write_text("A steady voice.", encoding="utf-8")
    core = {profile_home: ("- Ink.",)}
    monkeypatch.setattr("hermes_cli.heart.taste_context.core_lines", core.__getitem__)
    monkeypatch.setattr("agent.context_compressor.call_llm", lambda **kw: _mock_response("Fruit summary."))
    sid = "20261115_090000_aaaaaa"
    with SessionDB(db_path=profile_home / "state.db") as db, closing(_agent(sid, session_db=db)) as agent:
        db.ensure_session(sid, source="cli", model="test/model")
        for message in _exchanges(10):
            db.append_message(sid, message["role"], message["content"])
        cli = HermesCLI.__new__(HermesCLI)
        cli.agent, cli.session_id = agent, sid
        cli.conversation_history = db.get_messages_as_conversation(sid)
        monkeypatch.setattr(cli, "_busy_command", lambda *args, **kwargs: nullcontext())
        monkeypatch.setattr(agent, "_compression_feasibility_checked", True, raising=False)

        manager = MemoryManager()
        manager.initialize_all(sid, hermes_home=profile_home)
        provider = LinaMemoryProvider.__new__(LinaMemoryProvider)
        provider._policy = load_policy()
        provider._init = {"platform": "cli", "user_id": "" if owner else "relay"}
        monkeypatch.setattr(manager, "get_provider", lambda name: provider if name == "lina-memory" else None)
        monkeypatch.setattr(agent, "_memory_manager", manager)
        # Freeze the snapshot, then leave the turn and its scope before /compress.
        with owner_taste_prompt_scope(agent, profile_home if owner else None):
            before = agent._build_system_prompt()
            monkeypatch.setattr(agent, "_cached_system_prompt", before)
        db.update_system_prompt(sid, before)
        assert ("- Ink." in before) is owner
        monkeypatch.setattr(agent, "_owner_taste_allowed", not owner, raising=False)
        core[profile_home] = ("- Charcoal.",)

        cli._manual_compress("/compress")

        assert any("Fruit summary." in row["content"] for row in cli.conversation_history)
        session = db.get_session(cli.session_id)
        assert session is not None
        after = session["system_prompt"]
        assert before[:before.index(FRAME)] == after[:after.index(FRAME)]
        assert "- Ink." not in after
        assert ("- Charcoal." in after) is owner
        assert owner_taste_home(agent) is None
