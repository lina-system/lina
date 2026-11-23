"""Operator rollup requests obey the owning home's taste policy (#360)."""

from datetime import UTC, datetime
from importlib import import_module
from pathlib import Path

import pytest

LCM = "plugins.context_engine.lcm-x"
CLAUSE = "Do not record the agent's likes or dislikes as lasting traits; describe only what happened."


@pytest.mark.parametrize("profile", ["synthetic-owner", "synthetic-peer"])
def test_operator_rollup_request_uses_owning_taste_policy(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, memory_store_sql, profile: str,
) -> None:
    owner = tmp_path / "profiles" / "synthetic-owner"
    home = tmp_path / "profiles" / profile
    owner.mkdir(parents=True)
    home.mkdir(parents=True, exist_ok=True)
    memory_store_sql(
        "INSERT INTO memory.tastes (agent_id, target_kind, label, status, valence, strength, origin) "
        "VALUES (%s, 'topic', %s, 'active', 'like', 0.6, 'outcome')",
        ("synthetic-owner", "Blue Triangles"),
    )
    # The operator's ambient profile must not select the peer's policy.
    monkeypatch.setenv("HERMES_HOME", str(owner))
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
        engine._dag.add_node(import_module(f"{LCM}.dag").SummaryNode(
            session_id="synthetic-session", depth=0,
            summary="The assistant chose blue triangles for the sample on Tuesday.",
            token_count=30, created_at=stamp, earliest_at=stamp, latest_at=stamp,
        ))
        config.temporal_rollups_enabled = True

        result = import_module(f"{LCM}.command").handle_lcm_command(
            "rollups rebuild day 2026-07-15", engine,
        )

        assert "status: complete" in result
        assert requests
        assert all((CLAUSE in prompt) == (profile == "synthetic-owner") for prompt in requests)
        assert all("Keep dates." in prompt for prompt in requests)
        assert config.custom_instructions == "Keep dates."
    finally:
        engine.shutdown()
