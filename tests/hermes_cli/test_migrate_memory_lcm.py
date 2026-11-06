"""Copied LCM history uses the real store route and survives live writes."""

from contextlib import closing
import json
import sqlite3
import pytest

from tests.hermes_cli.test_migrate_memory import _command, _run
from tests.plugins.context_engine.test_lcm_x_on_store import _lcm
from plugins.context_engine import lcm_x_store
from hermes_state_store import MessageStore
from array import array


def test_cli_imports_lcm_graph_and_replays_after_live_writes(tmp_path, _memory_store, memory_store_sql):
    # Given: copied host messages plus matched, mismatched and LCM-only placements.
    command = _command(tmp_path, _memory_store)
    source = tmp_path / "hermes/profiles/alpha/lcm.db"
    before = source.read_bytes()
    # When: importing through the actual CLI.
    done = _run(command, tmp_path)
    assert done.returncode == 0, done.stdout + done.stderr
    report = json.loads(done.stdout)
    # Then: native graph identity, host text reuse and registry identity are durable.
    assert report["lcm"]["counted"] and report["lcm"]["ok"]
    assert report["complete"] and all(step["counted"] and step["ok"] for step in report["steps"].values())
    assert report["lcm"]["counts"]["messages"]["source"] == 3
    assert report["lcm"]["matched_text"] == 1
    assert report["lcm"]["inserted_text"] == 2
    assert memory_store_sql("SELECT extract(epoch FROM m.ts)::bigint FROM memory.messages m "
                            "JOIN ctx.lcm_messages p ON p.text_uid=m.uid WHERE p.store_id=3") == [(1767323048,)]
    assert memory_store_sql("SELECT node_id,source_ids FROM ctx.summary_nodes ORDER BY node_id") == [(1, "[1,2,3]"), (2, "[1]")]
    assert source.read_bytes() == before
    assert memory_store_sql("SELECT count(DISTINCT identity_id) FROM memory.identity_keys "
                            "WHERE (source,key) IN (('lcm_conversation','alpha-copy:conversation-a'),"
                            "('hermes_session','alpha-copy:session-a'))") == [(1,)]
    assert memory_store_sql("SELECT DISTINCT m.conversation_id=k.identity_id FROM ctx.lcm_messages m "
                            "JOIN memory.identity_keys k ON k.source='lcm_conversation' "
                            "AND k.key='alpha-copy:conversation-a'") == [(True,)]
    with closing(MessageStore("alpha-copy")) as text:
        uids = {r[0] for r in memory_store_sql("SELECT text_uid FROM ctx.lcm_messages")}
        assert text.held_by_context(list(uids)) == uids
        memory_store_sql("UPDATE memory.messages SET observed_at=to_timestamp(1600000000)")
        host_uids = text.all_uids() - uids
        assert text.collect_unreferenced(lambda page: (set(page) & host_uids) | text.held_by_context(page)) == 0
        assert text.held_by_context(list(uids)) == uids
    target = tmp_path / "lina/profiles/alpha-copy/lcm.db"
    lcm_x_store.route(target, "alpha-copy")
    try:
        with closing(_lcm("store").MessageStore(target)) as live:
            live.append("new-live", {"role": "user", "content": "new live LCM text"})
        repeated = _run(command, tmp_path)
        assert repeated.returncode == 0, repeated.stdout + repeated.stderr
        assert json.loads(repeated.stdout)["lcm"]["inserted"] == 0
        assert memory_store_sql("SELECT count(*) FROM ctx.lcm_messages") == [(4,)]
    finally:
        lcm_x_store.unroute(target)


def test_cli_refuses_dangling_summary_before_publishing_lcm(tmp_path, _memory_store, memory_store_sql):
    # Given: an invalid copied summary edge.
    command = _command(tmp_path, _memory_store)
    with closing(sqlite3.connect(tmp_path / "hermes/profiles/alpha/lcm.db")) as db:
        db.execute("UPDATE summary_nodes SET source_ids='[999]' WHERE node_id=1")
        db.commit()
    # When / Then: no successful report or partial LCM graph.
    result = _run(command, tmp_path)
    assert result.returncode == 1
    assert "reference" in json.loads(result.stdout)["error"]
    assert memory_store_sql("SELECT count(*) FROM ctx.lcm_messages") == [(0,)]


def test_real_schema_vectors_provenance_and_corruption_refusal(tmp_path, _memory_store, memory_store_sql):
    command = _command(tmp_path, _memory_store)
    path = tmp_path / "hermes/profiles/alpha/lcm.db"
    with closing(_lcm("store").MessageStore(path)), closing(_lcm("dag").SummaryDAG(path)):
        pass
    with closing(sqlite3.connect(path)) as db:
        bootstrap = _lcm("db_bootstrap")
        bootstrap.ensure_embedding_tables(db)
        db.execute("CREATE TABLE IF NOT EXISTS summary_node_provenance "
                   "(node_id INTEGER PRIMARY KEY, escalation_level INTEGER,model TEXT,created_at REAL)")
        db.execute("INSERT INTO summary_node_provenance VALUES (2,1,'synthetic',1767323050)")
        for key, model, width in (("good", "qwen3-embedding-8b", 1024), ("old", "other-model", 1536)):
            db.execute("INSERT INTO lcm_embedding_profile(identity_hash,provider,model_name,dim) VALUES (?,?,?,?)",
                       (key, "synthetic", model, width))
            db.execute("INSERT INTO lcm_embedding_vectors VALUES (?,?,?)", ("1", key, array("f", [0.5] * width).tobytes()))
        db.commit()
    result = _run(command, tmp_path)
    assert result.returncode == 0, result.stdout + result.stderr
    report = json.loads(result.stdout)["lcm"]
    assert report["counts"]["lcm_embedding_vectors"] == {"source": 2, "matched": 0, "inserted": 1, "skipped": 1}
    repeated = _run(command, tmp_path)
    assert repeated.returncode == 0, repeated.stdout + repeated.stderr
    assert json.loads(repeated.stdout)["lcm"]["identity_maps"] == report["identity_maps"]
    assert memory_store_sql("SELECT model,dim FROM vec.embeddings WHERE kind='summary'") == [("qwen3-embedding-8b", 1024)]
    assert memory_store_sql("SELECT node_id,model FROM ctx.summary_node_provenance") == [(2, "synthetic")]
    memory_store_sql("UPDATE ctx.summary_nodes SET summary='corrupt' WHERE node_id=1")
    again = _run(command, tmp_path)
    assert again.returncode == 1
    assert "changed imported LCM row" in json.loads(again.stdout)["error"]


def test_two_agents_keep_scoped_native_ids_and_distinct_text(tmp_path, _memory_store, memory_store_sql):
    command = _command(tmp_path, _memory_store)
    first = _run(command, tmp_path)
    assert first.returncode == 0, first.stdout + first.stderr
    second_command = list(command)
    second_command[second_command.index("--as") + 1] = "second-copy"
    second_command[second_command.index("--work-dir") + 1] = str(tmp_path / "second-copy-work")
    second = _run(second_command, tmp_path)
    assert second.returncode == 0, second.stdout + second.stderr
    assert memory_store_sql("SELECT agent_id,count(*),min(store_id),max(store_id) FROM ctx.lcm_messages "
                            "GROUP BY agent_id ORDER BY agent_id") == [("alpha-copy", 3, 1, 3), ("second-copy", 3, 1, 3)]
    assert memory_store_sql("SELECT count(*) FROM ctx.lcm_messages p JOIN memory.messages m ON m.uid=p.text_uid "
                            "WHERE p.agent_id<>m.agent_id") == [(0,)]


def test_interrupted_graph_rolls_back_and_reuses_committed_text(tmp_path, _memory_store, memory_store_sql, monkeypatch):
    from hermes_cli.migrate_memory_agent import migrate_agent
    from hermes_cli import migrate_memory_lcm as migration

    _command(tmp_path, _memory_store)
    source = tmp_path / "hermes/profiles/alpha"
    destination = tmp_path / "lina/profiles/alpha-copy"
    migrate_agent(source / "state.db", destination / "state.db", "alpha-copy")
    original = migration._insert

    def interrupted(conn, table, row):
        if table == "summary_nodes":
            raise ValueError("injected summary failure")
        return original(conn, table, row)

    with monkeypatch.context() as patcher:
        patcher.setattr(migration, "_insert", interrupted)
        with pytest.raises(ValueError, match="injected summary failure"):
            migration.migrate_lcm(source / "lcm.db", source / "state.db", destination, "alpha-copy", tmp_path)
    assert memory_store_sql("SELECT count(*) FROM ctx.lcm_messages") == [(0,)]
    text_count = memory_store_sql("SELECT count(*) FROM memory.messages")
    report = migration.migrate_lcm(source / "lcm.db", source / "state.db", destination, "alpha-copy", tmp_path)
    assert report["ok"] and report["inserted"] == 5
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == text_count


def test_summary_cycle_is_refused_before_any_lcm_rows(tmp_path, _memory_store, memory_store_sql):
    command = _command(tmp_path, _memory_store)
    with closing(sqlite3.connect(tmp_path / "hermes/profiles/alpha/lcm.db")) as source:
        source.execute("UPDATE summary_nodes SET source_type='nodes',source_ids='[2]' WHERE node_id=1")
        source.commit()
    result = _run(command, tmp_path)
    assert result.returncode == 1
    assert "cyclic LCM summary reference" in json.loads(result.stdout)["error"]
    assert memory_store_sql("SELECT count(*) FROM ctx.summary_nodes") == [(0,)]
