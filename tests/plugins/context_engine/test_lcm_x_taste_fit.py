"""The host adopts a fitted context and its emission proof after a taste repair."""

import hashlib
from importlib import import_module
from pathlib import Path

import pytest

from hermes_cli.heart.forget import forget_from_conversation

LCM = "plugins.context_engine.lcm-x"
SHORT = "The sample ships Friday."
LONG = SHORT + " The shipment needs a tracking number and a signed receipt." * 4


@pytest.mark.parametrize("replacement", [SHORT, LONG], ids=["shorter", "longer"])
def test_fitted_context_and_proof_survive_taste_repair(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, memory_store_sql, replacement: str,
) -> None:
    home = tmp_path / "profiles" / "synthetic-owner"
    home.mkdir(parents=True)
    monkeypatch.setenv("HERMES_HOME", str(home))
    (home / "config.yaml").write_text("lcm:\n  store_backend: sqlite\n")
    memory_store_sql(
        "INSERT INTO memory.tastes (agent_id, target_kind, label, status, valence, strength, origin) "
        "VALUES ('synthetic-owner', 'topic', 'watercolor', 'active', 'like', 0.6, 'outcome')"
    )
    config = import_module(f"{LCM}.config").LCMConfig(
        database_path=str(home / "lcm.db"), embeddings_enabled=False, temporal_rollups_enabled=False,
        fresh_tail_count=32, survival_reserve=0, threshold_full_sweep_enabled=False,
    )
    engine = import_module(f"{LCM}.engine").LCMEngine(config=config, hermes_home=str(home))
    session = "sample-session"
    original = "The assistant likes watercolor. " + SHORT
    try:
        engine.on_session_start(session, conversation_id=session, platform="cli", context_length=12000)
        [source_id] = engine._store.append_batch(
            session, [{"role": "assistant", "content": original + LONG * 3}], source="cli",
        )
        node_id = engine._dag.add_node(import_module(f"{LCM}.dag").SummaryNode(
            session_id=session, summary=original, expand_hint="Shipment details",
            token_count=import_module(f"{LCM}.tokens").count_tokens(original), source_ids=[source_id],
        ))
        tail = [
            {"role": role, "content": f"{index}. " + "Shipment records and delivery status. " * 12}
            for index in range(4) for role in ("user", "assistant")
        ]
        before = engine._assemble_context(None, tail, include_lcm_note=False, persist=False)
        # The fit must drop one whole turn. Its remaining summary and tail have one token to spare.
        one_turn_removed = engine._assemble_context(None, tail[2:], include_lcm_note=False, persist=False)
        budget = engine._survival_measure(one_turn_removed) + 1
        assert engine._survival_measure(before) > budget
        engine.context_length = budget
        forgotten = forget_from_conversation(
            "forget the taste for watercolor", owner_verified=True, history=[], target_home=home,
        )
        assert forgotten is not None and forgotten.forgotten is not None

        def summarize(**request):
            assert original in request["text"]
            return replacement, 1

        monkeypatch.setattr(import_module(f"{LCM}.taste_recondense"), "summarize_with_escalation", summarize)
        after = engine.compress(before, force=True)

        assert engine._survival_measure(after) <= budget
        assert all("watercolor" not in row["content"] for row in after)
        repaired = engine._dag.get_node(node_id).summary
        if replacement == SHORT:
            assert len(after) == len(one_turn_removed)
        assert repaired == replacement
        proof = engine._compress_commit_proof
        assert proof["output"] == [engine._proof_replay_identity(row, strip_carrier=False) for row in after]
        assert proof["emissions"]
        for emission in proof["emissions"]:
            row = after[emission["output_occurrence"]["index"]]
            span = row["content"].encode()[:emission["generated_span_bytes"]]
            assert hashlib.sha256(span).hexdigest() == emission["generated_span_sha256"]
        assert any(repaired in row["content"] for row in after)
    finally:
        engine.shutdown()
