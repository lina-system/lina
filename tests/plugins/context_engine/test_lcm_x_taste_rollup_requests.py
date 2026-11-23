"""Ordinary summary requests obey the owning home's taste history (#360)."""

from datetime import UTC, datetime
from importlib import import_module
from pathlib import Path

import pytest

from hermes_cli.heart.forget import forget_from_conversation
from hermes_cli.heart.taste_context import has_tastes

LCM = "plugins.context_engine.lcm-x"
CLAUSE = "Do not record the agent's likes or dislikes as lasting traits; describe only what happened."


@pytest.mark.parametrize("surface", ["instructions", "leaf", "condensation", "rollup"])
@pytest.mark.parametrize("history", ["live", "forgotten", "never"])
def test_summary_request_uses_owning_taste_history(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, memory_store_sql, history: str, surface: str,
) -> None:
    owner = tmp_path / "profiles" / "synthetic-owner"
    home = owner if history != "never" else tmp_path / "profiles" / "synthetic-peer"
    owner.mkdir(parents=True)
    home.mkdir(parents=True, exist_ok=True)
    memory_store_sql(
        "INSERT INTO memory.tastes (agent_id, target_kind, label, status, valence, strength, origin) "
        "VALUES (%s, 'topic', %s, 'active', 'like', 0.6, 'outcome')",
        ("synthetic-owner", "Blue Triangles"),
    )
    # The operator's ambient profile must not select the peer's policy.
    monkeypatch.setenv("HERMES_HOME", str(owner))
    if history != "live":
        result = forget_from_conversation(
            "Forget the taste for Blue Triangles", owner_verified=True, history=[], target_home=owner,
        )
        assert result is not None and result.forgotten is not None
    assert has_tastes(home) == (history == "live")
    expected = history != "never"
    if surface == "instructions":
        instructions = import_module(f"{LCM}.taste_forget").taste_instructions(home, "Keep dates.")
        assert instructions == ("Keep dates.\n" + CLAUSE if expected else "Keep dates.")
        return
    config = import_module(f"{LCM}.config").LCMConfig(
        database_path=str(home / "lcm.db"), embeddings_enabled=False,
        temporal_rollups_enabled=False, custom_instructions="Keep dates.", l3_truncate_tokens=1,
    )
    engine = import_module(f"{LCM}.engine").LCMEngine(config=config, hermes_home=str(home))
    requests: list[str] = []

    def respond(prompt: str, max_tokens: int, **route) -> str:
        requests.append(prompt)
        return "The assistant chose a shape for the sample on Tuesday."

    monkeypatch.setattr(import_module(f"{LCM}.escalation"), "_call_llm_for_summary", respond)
    try:
        engine.on_session_start("synthetic-session")
        stamp = datetime(2026, 7, 15, 12, tzinfo=UTC).timestamp()
        leaf = import_module(f"{LCM}.dag").SummaryNode(
            session_id="synthetic-session", depth=0,
            summary="The assistant chose blue triangles for the sample on Tuesday.",
            token_count=30, created_at=stamp, earliest_at=stamp, latest_at=stamp,
        )
        if surface == "leaf":
            engine._summarize_leaf_chunk_with_rescue([{"role": "assistant", "content": leaf.summary}])
        elif surface == "condensation":
            engine._dag.add_node(leaf)
            engine._condense_summary_nodes([leaf])
        else:
            engine._dag.add_node(leaf)
            config.temporal_rollups_enabled = True
            reply = import_module(f"{LCM}.command").handle_lcm_command("rollups rebuild day 2026-07-15", engine)
            assert "status: complete" in reply
        assert requests
        assert all((CLAUSE in prompt) == expected for prompt in requests)
        assert all("Keep dates." in prompt for prompt in requests)
        assert config.custom_instructions == "Keep dates."
    finally:
        engine.shutdown()
