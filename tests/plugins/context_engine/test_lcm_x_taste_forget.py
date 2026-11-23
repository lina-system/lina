"""A taste tombstone recondenses the owning LCM-X DAG at compression (#360)."""

import threading
from contextlib import closing
from importlib import import_module
from pathlib import Path

import pytest

from plugins.context_engine import lcm_x_store

LCM = "plugins.context_engine.lcm-x"
SESSION = "synthetic-session"
TARGET = "Blue Triangles"
FACT = "The sample was filed on Tuesday."
CLAUSE = "Do not record the agent's likes or dislikes as lasting traits; describe only what happened."


@pytest.mark.parametrize("backend", ["sqlite", "postgres"])
def test_next_compression_recondenses_both_depths_for_the_owning_home(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, memory_store_sql, backend: str,
) -> None:
    home = tmp_path / "profiles" / "synthetic-owner"
    home.mkdir(parents=True)
    (home / "config.yaml").write_text(f"lcm:\n  store_backend: {backend}\n", encoding="utf-8")
    other = tmp_path / "profiles" / "synthetic-peer"
    other.mkdir(parents=True)
    (other / "config.yaml").write_text(f"lcm:\n  store_backend: {backend}\n", encoding="utf-8")
    [(taste_id,)] = memory_store_sql(
        "INSERT INTO memory.tastes (agent_id, target_kind, label, status, valence, strength, origin) "
        "VALUES (%s, 'topic', %s, 'active', 'like', 0.6, 'outcome') RETURNING id",
        ("synthetic-owner", TARGET),
    )
    config = import_module(f"{LCM}.config").LCMConfig
    engine_type = import_module(f"{LCM}.engine").LCMEngine
    node_type = import_module(f"{LCM}.dag").SummaryNode
    escalation = import_module(f"{LCM}.escalation")
    requests: list[str] = []

    def respond(prompt: str, max_tokens: int, **route) -> str:
        requests.append(prompt)
        if CLAUSE in prompt:
            return f"The assistant chose a shape in the example. {FACT}"
        return "The assistant always prefers blue triangles."

    monkeypatch.setattr(escalation, "_call_llm_for_summary", respond)
    monkeypatch.setenv("HERMES_HOME", str(home))
    cfg = config(embeddings_enabled=False, l3_truncate_tokens=1)
    engine = engine_type(config=cfg, hermes_home=str(home))
    peer = engine_type(config=cfg, hermes_home=str(other))
    resume_config = config(
        embeddings_enabled=False, temporal_rollups_enabled=False, l3_truncate_tokens=1,
        fresh_tail_count=2, leaf_chunk_tokens=350, condensation_fanin=20, threshold_full_sweep_enabled=False,
    )
    resumed = engine_type(config=resume_config, hermes_home=str(home))
    try:
        engine.on_session_start(SESSION)
        peer.on_session_start("synthetic-peer-session")
        resumed.on_session_start(
            "synthetic-resumed-session", conversation_id="synthetic-resumed-session",
            platform="cli", context_length=12000,
        )
        raw = [
            {"role": "user", "content": "Pick a shape and record where the sample was filed."},
            {"role": "assistant", "content": f"For this example I picked blue triangles. {FACT}"},
        ]
        raw_ids = engine._store.append_batch(SESSION, raw, source="cli")
        original = f"The assistant always prefers blue triangles. {FACT}"
        leaf_id = engine._dag.add_node(node_type(
            session_id=SESSION, summary=original, token_count=30, source_token_count=60, source_ids=raw_ids,
        ))
        upper_id = engine._dag.add_node(node_type(
            session_id=SESSION, depth=1, summary=original, token_count=30,
            source_token_count=30, source_ids=[leaf_id], source_type="nodes",
        ))
        unrelated_id = engine._dag.add_node(node_type(
            session_id=SESSION, summary="The archive opens on Friday.", token_count=10,
        ))
        peer_id = peer._dag.add_node(node_type(
            session_id="synthetic-peer-session", summary=original, token_count=30,
        ))
        resumed_raw_ids = resumed._store.append_batch("synthetic-resumed-session", raw, source="cli")
        resumed_id = resumed._dag.add_node(node_type(
            session_id="synthetic-resumed-session", summary=original, token_count=30,
            source_token_count=60, source_ids=resumed_raw_ids,
        ))

        system = {"role": "system", "content": "Frozen synthetic prefix."}
        fresh = [{"role": "user", "content": "What is the latest status?"}]
        before = engine._assemble_context(system, fresh, include_lcm_note=False, persist=False)
        resume_history = [system]
        for index in range(8):
            resume_history.extend([
                {"role": "user", "content": f"Input {index}. " + "Synthetic project and layout notes. " * 30,
                 "timestamp": float(2 * index + 1)},
                {"role": "assistant", "content": f"Reply {index}. " + "Synthetic sample and archive decisions. " * 30,
                 "timestamp": float(2 * index + 2)},
            ])
        old_resumed_prefix = resumed.compress(resume_history, force=True)
        assert resumed._compress_commit_proof["emissions"]
        resumed.shutdown()
        resumed = engine_type(config=resume_config, hermes_home=str(home))
        resumed.on_session_start(
            "synthetic-resumed-session", conversation_id="synthetic-resumed-session",
            platform="cli", context_length=12000,
        )
        assert any(original in row["content"] for row in before)
        assert any(original in row["content"] for row in old_resumed_prefix)

        forget = import_module(f"{LCM}.taste_forget").queue_taste_forget
        forget(hermes_home=home, target=TARGET, taste_id=taste_id)
        memory_store_sql(
            "UPDATE memory.tastes SET status='forgotten', forgotten_by='owner' WHERE agent_id=%s AND id=%s",
            ("synthetic-owner", taste_id),
        )
        assert engine._assemble_context(system, fresh, include_lcm_note=False, persist=False) == before

        after = engine.compress(before, force=True)

        assert after[0] == system
        assert all(original not in row["content"] for row in after)
        for node_id in (upper_id, leaf_id):
            node = engine._dag.get_node(node_id)
            assert node is not None and "blue triangles" not in node.summary.casefold()
            assert FACT in node.summary
            assert engine._dag.source_message_ids(node_id, limit=10) == raw_ids
        stored = engine._store.get_batch(raw_ids)
        assert [{key: stored[row_id][key] for key in ("role", "content")} for row_id in raw_ids] == raw
        assert engine._dag.get_node(unrelated_id).summary == "The archive opens on Friday."
        assert peer._dag.get_node(peer_id).summary == original
        assert any(CLAUSE in prompt for prompt in requests)
        assert resumed._dag.get_node(resumed_id).summary == original
        resumed_after = resumed.compress(old_resumed_prefix, force=True)
        assert all(original not in row["content"] for row in resumed_after)
        assert resumed._dag.source_message_ids(resumed_id, limit=10) == resumed_raw_ids
        resumed_raw = resumed._store.get_session_messages("synthetic-resumed-session")
        assert all(original not in row["content"] for row in resumed_raw)
        if backend == "postgres":
            connection = lcm_x_store.backend_for(home / "lcm.db")
            assert connection is not None
            with closing(connection()) as conn:
                assert conn.agent_id == "synthetic-owner"
        else:
            assert (home / "lcm.db").is_file()
    finally:
        engine.shutdown()
        peer.shutdown()
        resumed.shutdown()
        lcm_x_store.unroute(home / "lcm.db")
        lcm_x_store.unroute(other / "lcm.db")


def test_ordinary_condensation_uses_taste_instruction_for_owner_only(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, memory_store_sql,
) -> None:
    owner = tmp_path / "profiles" / "synthetic-owner"
    peer = tmp_path / "profiles" / "synthetic-peer"
    for home in (owner, peer):
        home.mkdir(parents=True)
    memory_store_sql(
        "INSERT INTO memory.tastes (agent_id, target_kind, label, status, valence, strength, origin) "
        "VALUES (%s, 'topic', %s, 'active', 'like', 0.6, 'outcome')",
        ("synthetic-owner", TARGET),
    )
    config = import_module(f"{LCM}.config").LCMConfig
    engine_type = import_module(f"{LCM}.engine").LCMEngine
    node_type = import_module(f"{LCM}.dag").SummaryNode
    escalation = import_module(f"{LCM}.escalation")
    requests: list[str] = []

    caller = threading.get_ident()

    def respond(prompt: str, max_tokens: int, **route) -> str:
        # An engines own background rollup maintenance may call the summariser after the next homes loop has
        # cleared the list; only the condensation this test drives (on its own thread) is the evidence.
        if threading.get_ident() == caller:
            requests.append(prompt)
        return "The assistant chose a shape in the example."

    monkeypatch.setattr(escalation, "_call_llm_for_summary", respond)
    # Deliberately leave the ambient home set to the owner during the peer's call.
    monkeypatch.setenv("HERMES_HOME", str(owner))
    by_home: dict[Path, list[str]] = {}
    for home in (owner, peer):
        requests.clear()
        engine = engine_type(
            config=config(embeddings_enabled=False, l3_truncate_tokens=1, custom_instructions="Keep dates."),
            hermes_home=str(home),
        )
        try:
            engine.on_session_start(SESSION)
            leaf = node_type(
                session_id=SESSION,
                summary=f"The assistant chose blue triangles for the example. {FACT}",
                token_count=30,
            )
            engine._dag.add_node(leaf)
            engine._condense_summary_nodes([leaf])
            by_home[home] = requests.copy()
        finally:
            engine.shutdown()

    assert by_home[owner] and by_home[peer]
    assert all(CLAUSE in prompt for prompt in by_home[owner])
    assert all(CLAUSE not in prompt for prompt in by_home[peer])
    assert all("Keep dates." in prompt for prompts in by_home.values() for prompt in prompts)


@pytest.mark.parametrize("failure", ["repeat", "budget"])
def test_failed_resummary_never_returns_the_retired_taste(
    failure: str, tmp_path: Path, monkeypatch: pytest.MonkeyPatch, memory_store_sql,
) -> None:
    home = tmp_path / "profiles" / "synthetic-fallback"
    home.mkdir(parents=True)
    database = tmp_path / "scoped-lcm.sqlite"
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("LCM_DATABASE_PATH", str(database))
    [(taste_id,)] = memory_store_sql(
        "INSERT INTO memory.tastes (agent_id, target_kind, label, status, valence, strength, origin) "
        "VALUES (%s, 'topic', %s, 'active', 'like', 0.6, 'outcome') RETURNING id",
        ("synthetic-fallback", TARGET),
    )
    config = import_module(f"{LCM}.config").LCMConfig
    engine_type = import_module(f"{LCM}.engine").LCMEngine
    node_type = import_module(f"{LCM}.dag").SummaryNode
    escalation = import_module(f"{LCM}.escalation")
    repair = import_module(f"{LCM}.taste_recondense")
    requests: list[str] = []

    def unavailable(**request) -> tuple[str, int]:
        requests.append(request["text"])
        if failure == "budget":
            raise escalation.SweepBudgetExhausted("test budget stop")
        return "The assistant prefers blue triangles.", 1

    monkeypatch.setattr(repair, "summarize_with_escalation", unavailable)
    engine = engine_type(
        config=config(embeddings_enabled=False, database_path=str(database)), hermes_home=str(home),
    )
    try:
        engine.on_session_start(SESSION)
        [raw_id] = engine._store.append_batch(
            SESSION, [{"role": "assistant", "content": "I picked blue triangles for this example."}], source="cli",
        )
        node_id = engine._dag.add_node(node_type(
            session_id=SESSION, summary=f"The assistant prefers blue triangles. {FACT}",
            expand_hint=f"Blue triangles. {FACT}", source_ids=[raw_id], token_count=30,
        ))
        before = engine._assemble_context(
            {"role": "system", "content": "Frozen prefix."}, [],
            include_lcm_note=False, persist=False,
        )
        import_module(f"{LCM}.taste_forget").queue_taste_forget(
            hermes_home=home, target=TARGET, taste_id=taste_id,
        )
        memory_store_sql(
            "UPDATE memory.tastes SET status='forgotten', forgotten_by='owner' WHERE agent_id=%s AND id=%s",
            ("synthetic-fallback", taste_id),
        )

        after = engine.compress(before, force=True)

        assert requests
        assert all("blue triangles" not in row["content"].casefold() for row in after)
        assert any(FACT in row["content"] for row in after)
        node = engine._dag.get_node(node_id)
        assert node.summary == node.expand_hint == FACT
        assert engine._dag.source_message_ids(node_id, limit=10) == [raw_id]
        assert engine._store.get(raw_id)["content"] == "I picked blue triangles for this example."
        assert database.is_file() and not (home / "lcm.db").exists()
    finally:
        engine.shutdown()
