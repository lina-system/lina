"""ACP restores alternation-clean live history."""
import itertools
import pytest
from hermes_state import SessionDB


@pytest.fixture
def db(tmp_path):
    session_db = SessionDB(db_path=tmp_path / 'state.db')
    yield session_db
    session_db.close()

def _seed_restore_history(db, session_id="acp1"):
    db.create_session(session_id, "acp")
    db.append_message(session_id=session_id, role="user", content="first ask")
    db.append_message(session_id=session_id, role="assistant", content="first reply")
    db.append_message(session_id=session_id, role="user", content="unanswered turn")
    db.append_message(session_id=session_id, role="user", content="next turn")
    db.append_message(session_id=session_id, role="assistant", content="next reply")


def test_restore_merges_adjacent_user_rows_for_live_replay(db):
    """acp_adapter.SessionManager._restore feeds LIVE REPLAY: the loaded history
    becomes the resumed agent's working conversation. It must be alternation-
    clean so the pre-request repair doesn't re-fire every turn."""
    from acp_adapter.session import SessionManager

    _seed_restore_history(db, "acp1")

    class _StubAgent:
        model = "stub"

    mgr = SessionManager(agent_factory=lambda: _StubAgent(), db=db)
    state = mgr._restore("acp1")

    assert state is not None
    roles = [m["role"] for m in state.history]
    # No consecutive user turns — the durable user;user wedge was healed.
    assert roles == ["user", "assistant", "user", "assistant"], roles
    for a, b in itertools.pairwise(roles):
        assert not (a == "user" and b == "user"), "unhealed user;user in ACP live replay"
    # No user input lost — both user texts survive, merged in order.
    merged = state.history[2]["content"]
    assert "unanswered turn" in merged and "next turn" in merged
