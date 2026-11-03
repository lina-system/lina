"""A missing memory store must reach the compaction error path (lina#173)."""

from contextlib import closing
import copy
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from hermes_state import SessionDB
from hermes_state_store import StoreUnavailableError, resolve_dsn as installed_store_dsn
from run_agent import AIAgent


@pytest.mark.parametrize("in_place", [True, False], ids=["in-place", "rotation"])
def test_unavailable_store_aborts_compaction(tmp_path: Path, monkeypatch: pytest.MonkeyPatch, in_place: bool):
    # Given a real SessionDB whose text store cannot connect, and a deterministic
    # summary provider; no fake persistence implementation can hide the failure.
    with closing(SessionDB(db_path=tmp_path / "state.db")) as db:
        db.create_session("parent", source="cli")
        monkeypatch.setattr("hermes_state_store.resolve_dsn", installed_store_dsn)
        monkeypatch.delenv("LINA_STORE_DSN", raising=False)
        agent = AIAgent(
            api_key="test-key", base_url="https://openrouter.ai/api/v1", model="test/model",
            quiet_mode=True, session_db=db, session_id="parent", skip_context_files=True, skip_memory=True,
        )
        monkeypatch.setattr(agent, "compression_in_place", in_place)
        monkeypatch.setattr(agent, "_compression_feasibility_checked", True, raising=False)
        compressor = MagicMock()
        compressor.compress.return_value = [
            {"role": "user", "content": "[CONTEXT COMPACTION] summary"},
            {"role": "assistant", "content": "tail"},
        ]
        compressor.compression_count = 1
        compressor.last_prompt_tokens = 0
        compressor.last_completion_tokens = 0
        compressor._last_summary_error = None
        compressor._last_compress_aborted = False
        compressor._last_aux_model_failure_model = None
        compressor._last_aux_model_failure_error = None
        monkeypatch.setattr(agent, "context_compressor", compressor)
        messages = [{"role": "user", "content": f"turn {i}: " + "original " * 100} for i in range(20)]
        before = copy.deepcopy(messages)

        # When compaction attempts to publish its summary, the real store fails.
        with pytest.raises(StoreUnavailableError):
            agent._compress_context(messages, "sys", approx_tokens=120_000, force=True)

        # Then neither the live transcript nor the durable parent is replaced.
        assert messages == before
        assert vars(agent)["session_id"] == "parent"
        parent = db.get_session("parent")
        assert parent is not None and parent["ended_at"] is None
        conn = db._conn
        assert conn is not None
        assert conn.execute("SELECT COUNT(*) FROM sessions").fetchone()[0] == 1
        assert conn.execute("SELECT COUNT(*) FROM transcript_rows").fetchone()[0] == 0
        assert db.get_compression_lock_holder("parent") is None
