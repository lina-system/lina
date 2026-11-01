"""ACP preview uses the shared manual compression contract."""
import copy
from unittest.mock import MagicMock

from tests.agent.test_conversation_compression_manual import _agent, _history

def test_acp_honours_preview_without_compressing():
    agent, history = _agent(), _history()
    frozen = copy.deepcopy(history)
    from acp_adapter.commands import SlashCommandsMixin
    acp = SlashCommandsMixin.__new__(SlashCommandsMixin)
    acp.session_manager = MagicMock()
    state = MagicMock(history=history, agent=agent, session_id="acp-sid")
    assert "Preview" in acp._cmd_compress("--preview", state)
    assert state.history == frozen
    acp.session_manager.save_session.assert_not_called()
    agent._compress_context.assert_not_called()
