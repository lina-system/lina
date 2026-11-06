"""lina#12: renaming a profile moves its message text to the new agent (agent_id comes from the path)."""

import json
from pathlib import Path
from unittest.mock import patch

import pytest

from hermes_cli.profile_identity import migrate_profile_identity
from hermes_cli.profiles import create_profile, rename_profile
from hermes_state import SessionDB
from hermes_state_store import TEXT_CACHE
from tools.session_search_tool import session_search


@pytest.fixture()
def profile_env(tmp_path, monkeypatch):
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    default_home = tmp_path / ".hermes"
    default_home.mkdir(exist_ok=True)
    monkeypatch.setenv("HERMES_HOME", str(default_home))
    return default_home


def _seed(profile_dir: Path) -> None:
    db = SessionDB(profile_dir / "state.db")
    try:
        db.create_session("history", "cli")
        db.append_message("history", "user", content="where is the lighthouse logbook")
        db.append_message("history", "assistant", content="on the second shelf")
    finally:
        db.close()


def _agents(memory_store_sql) -> dict[str, list[str]]:
    rows = memory_store_sql("SELECT agent_id, uid FROM memory.messages ORDER BY uid")
    found: dict[str, list[str]] = {}
    for agent, uid in rows:
        found.setdefault(agent, []).append(uid)
    return found


def _rename(old: str, new: str) -> Path:
    with patch("hermes_cli.profiles.check_alias_collision", return_value="skip"):
        return rename_profile(old, new)


def test_a_renamed_profile_reads_its_history_and_the_old_agent_keeps_no_rows(profile_env, memory_store_sql):
    _seed(create_profile("harbor", no_alias=True))
    assert set(_agents(memory_store_sql)) == {"harbor"}

    new_dir = _rename("harbor", "lighthouse")

    TEXT_CACHE.clear()
    db = SessionDB(new_dir / "state.db")
    try:
        assert [m["content"] for m in db.get_messages_as_conversation("history")] == [
            "where is the lighthouse logbook", "on the second shelf"]
        found = json.loads(session_search(query="logbook", db=db))
        assert [r["session_id"] for r in found["results"]] == ["history"]
    finally:
        db.close()
    agents = _agents(memory_store_sql)
    assert set(agents) == {"lighthouse"}
    assert all(uid.startswith("agent:lighthouse:") for uid in agents["lighthouse"])


def test_running_the_rename_step_again_changes_nothing(profile_env, memory_store_sql):
    _seed(create_profile("harbor", no_alias=True))
    new_dir = _rename("harbor", "lighthouse")
    before = _agents(memory_store_sql)

    assert migrate_profile_identity("harbor", "lighthouse") is True

    assert _agents(memory_store_sql) == before
    TEXT_CACHE.clear()
    db = SessionDB(new_dir / "state.db")
    try:
        assert [m["content"] for m in db.get_messages_as_conversation("history")] == [
            "where is the lighthouse logbook", "on the second shelf"]
        assert db._conn.execute("SELECT count(*) FROM transcript_rows WHERE text_uid LIKE 'agent:harbor:%'"
                                ).fetchone()[0] == 0
    finally:
        db.close()


def test_an_interrupted_rename_is_repaired_by_running_the_step_again(profile_env, memory_store_sql):
    _seed(create_profile("harbor", no_alias=True))
    new_dir = _rename("harbor", "lighthouse")
    # As if the state.db rewrite never ran: the store moved, the transcript still names the old agent.
    db = SessionDB(new_dir / "state.db")
    try:
        db._conn.execute("UPDATE transcript_rows SET text_uid = replace(text_uid, 'agent:lighthouse:', 'agent:harbor:')")
        db._conn.commit()
    finally:
        db.close()

    assert migrate_profile_identity("harbor", "lighthouse") is True

    TEXT_CACHE.clear()
    db = SessionDB(new_dir / "state.db")
    try:
        assert [m["content"] for m in db.get_messages_as_conversation("history")] == [
            "where is the lighthouse logbook", "on the second shelf"]
    finally:
        db.close()


def test_rename_is_refused_while_the_store_is_unreachable(profile_env, monkeypatch, memory_store_sql):
    import hermes_state_store

    old_dir = create_profile("harbor", no_alias=True)
    _seed(old_dir)

    def unreachable():
        raise hermes_state_store.StoreUnavailableError("cannot reach the memory store: connection refused")

    monkeypatch.setattr(hermes_state_store, "resolve_owner_dsn", unreachable)
    with pytest.raises(hermes_state_store.StoreUnavailableError, match="cannot reach the memory store"):
        _rename("harbor", "lighthouse")

    assert old_dir.is_dir()
    assert not (old_dir.parent / "lighthouse").exists()
    assert set(_agents(memory_store_sql)) == {"harbor"}


def test_a_profile_without_message_text_renames_with_no_store(profile_env, monkeypatch):
    import hermes_state_store

    old_dir = create_profile("harbor", no_alias=True)
    db = SessionDB(old_dir / "state.db")
    try:
        db.create_session("empty", "cli")
    finally:
        db.close()

    def no_store():
        raise hermes_state_store.StoreUnavailableError("the memory store is not set up")

    monkeypatch.setattr(hermes_state_store, "resolve_owner_dsn", no_store)
    monkeypatch.setattr(hermes_state_store, "resolve_dsn", no_store)

    new_dir = _rename("harbor", "lighthouse")

    assert new_dir.is_dir() and not old_dir.exists()
    assert migrate_profile_identity("harbor", "lighthouse") is True


class _OneAxisClient:
    model = "qwen3-embedding-8b"
    dimensions = 1024

    def embed(self, texts):
        vectors = []
        for i, _text in enumerate(texts):
            vector = [0.0] * self.dimensions
            vector[i] = 1.0
            vectors.append(vector)
        return vectors


def _reembed(_memory_store):
    import psycopg

    from hermes_cli.store_reembed import reembed
    reembed(lambda: psycopg.connect(_memory_store.superuser_dsn(), autocommit=True), _OneAxisClient(), ["agent"],
            batch_size=10, out=lambda _line: None)


def _vec_owners(memory_store_sql) -> tuple[set, set]:
    chunks = {row[0] for row in memory_store_sql("SELECT agent_id FROM vec.chunks")}
    vectors = {row[0] for row in memory_store_sql("SELECT agent_id FROM vec.embeddings WHERE kind = 'chunk'")}
    return chunks, vectors


def _nearest(_memory_store, agent: str) -> list[str]:
    import psycopg

    from hermes_cli.memory_eval_search import Question, new_vector
    query = [0.0] * 1024
    query[0] = 1.0
    question = Question(id="q", source="agent", agent=agent, question="?", expected=("x",))
    with psycopg.connect(_memory_store.superuser_dsn(), autocommit=True) as conn:
        return new_vector(conn, query, _OneAxisClient.model, question)


def test_a_renamed_profile_keeps_its_vectors_under_the_new_agent(profile_env, _memory_store, memory_store_sql):
    _seed(create_profile("harbor", no_alias=True))
    _reembed(_memory_store)
    assert _vec_owners(memory_store_sql) == ({"harbor"}, {"harbor"})

    _rename("harbor", "lighthouse")

    assert _vec_owners(memory_store_sql) == ({"lighthouse"}, {"lighthouse"})
    assert _nearest(_memory_store, "lighthouse")[0].startswith("agent:lighthouse:")
    assert _nearest(_memory_store, "harbor") == []


def test_a_failed_rename_gives_the_vectors_back_to_the_old_agent(profile_env, _memory_store, memory_store_sql):
    from hermes_cli.profile_identity import text_moved_for_rename

    profile_dir = create_profile("harbor", no_alias=True)
    _seed(profile_dir)
    _reembed(_memory_store)
    with pytest.raises(OSError, match="directory move failed"):
        with text_moved_for_rename(profile_dir, "harbor", "lighthouse"):
            assert _vec_owners(memory_store_sql) == ({"lighthouse"}, {"lighthouse"})
            raise OSError("directory move failed")

    assert _vec_owners(memory_store_sql) == ({"harbor"}, {"harbor"})
    assert set(_agents(memory_store_sql)) == {"harbor"}


def test_migrate_identity_moves_vectors_a_rename_left_behind(profile_env, _memory_store, memory_store_sql):
    _seed(create_profile("harbor", no_alias=True))
    _reembed(_memory_store)
    _rename("harbor", "lighthouse")
    # As a rename before vectors followed left them: text on the new agent, vectors on the old one.
    memory_store_sql("UPDATE vec.chunks SET agent_id = 'harbor'")
    memory_store_sql("UPDATE vec.embeddings SET agent_id = 'harbor' WHERE kind = 'chunk'")

    assert migrate_profile_identity("harbor", "lighthouse") is True

    assert _vec_owners(memory_store_sql) == ({"lighthouse"}, {"lighthouse"})


def test_a_store_without_vec_tables_still_renames(profile_env, memory_store_sql):
    _seed(create_profile("harbor", no_alias=True))
    memory_store_sql("ALTER SCHEMA vec RENAME TO vec_hidden")
    try:
        _rename("harbor", "lighthouse")
    finally:
        memory_store_sql("ALTER SCHEMA vec_hidden RENAME TO vec")
    assert set(_agents(memory_store_sql)) == {"lighthouse"}


_LCM_PLUGIN = Path(__file__).resolve().parents[2] / "plugins" / "context_engine" / "lcm-x"
_LCM_PACKAGE = "lina_lcm_x_rename"


def _lcm(module: str):
    import importlib
    import importlib.util
    import sys
    if _LCM_PACKAGE not in sys.modules:
        spec = importlib.util.spec_from_file_location(_LCM_PACKAGE, _LCM_PLUGIN / "__init__.py",
                                                      submodule_search_locations=[str(_LCM_PLUGIN)])
        package = importlib.util.module_from_spec(spec)
        package.__path__ = [str(_LCM_PLUGIN)]
        sys.modules[_LCM_PACKAGE] = package
    return importlib.import_module(f"{_LCM_PACKAGE}.{module}")


def _lcm_engine(profile_dir: Path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(profile_dir))  # where the engine reads lcm.store_backend
    # No temporal rollups: their maintenance thread outlives shutdown() and would write into the next test's store.
    return _lcm("engine").LCMEngine(config=_lcm("config").LCMConfig(embeddings_enabled=False,
                                                                     temporal_rollups_enabled=False),
                                    hermes_home=str(profile_dir))


def _seed_lcm(profile_dir: Path, memory_store_sql, monkeypatch) -> int:
    """An LCM-X home on the store (lcm.store_backend: postgres) with two messages, a summary of them and the
    summary's vector; returns the summary's node id."""
    import time

    from plugins.context_engine import lcm_x_store
    (profile_dir / "config.yaml").write_text("lcm:\n  store_backend: postgres\n")
    engine = _lcm_engine(profile_dir, monkeypatch)
    try:
        engine.on_session_start("lcm", conversation_id="c1")
        ids = engine._store.append_batch("lcm", [{"role": "user", "content": "the harbor tide table is in drawer 4"},
                                                 {"role": "assistant", "content": "noted: drawer 4"}])
        node_id = engine._dag.add_node(_lcm("dag").SummaryNode(
            session_id="lcm", summary="where the tide table is kept", source_ids=ids, token_count=6,
            created_at=time.time()))
    finally:
        engine.shutdown()
        lcm_x_store.unroute(profile_dir / "lcm.db")
        monkeypatch.setenv("HERMES_HOME", str(profile_dir.parent.parent))
    memory_store_sql("INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) "
                     "VALUES ('summary', %s, 'harbor', 'qwen3-embedding-8b', 1024, %s::vector)",
                     (node_id, "[" + ",".join(["0"] * 1023 + ["1"]) + "]"))
    return node_id


def _lcm_owners(memory_store_sql) -> dict[str, set]:
    """Who holds the LCM records: the agents of ctx's placements and summaries, of the summary vectors, and the
    agent each placement's text uid names."""
    return {
        "placements": {r[0] for r in memory_store_sql("SELECT agent_id FROM ctx.lcm_messages")},
        "summaries": {r[0] for r in memory_store_sql("SELECT agent_id FROM ctx.summary_nodes")},
        "summary vectors": {r[0] for r in memory_store_sql("SELECT agent_id FROM vec.embeddings WHERE kind = 'summary'")},
        "text uids": {r[0].split(":")[1] for r in memory_store_sql("SELECT text_uid FROM ctx.lcm_messages")},
    }


def test_a_renamed_store_profile_keeps_its_lcm_summaries_and_their_text(profile_env, memory_store_sql, monkeypatch):
    """Codex P1 on #242: a profile on the store (lcm.store_backend: postgres) keeps its LCM records in ctx under
    its agent; a rename moves them, the summary vectors and the placements' text uids with the text."""
    from plugins.context_engine import lcm_x_store
    profile_dir = create_profile("harbor", no_alias=True)
    _seed(profile_dir)
    node_id = _seed_lcm(profile_dir, memory_store_sql, monkeypatch)
    assert set(_lcm_owners(memory_store_sql)["placements"]) == {"harbor"}

    new_dir = _rename("harbor", "lighthouse")

    assert _lcm_owners(memory_store_sql) == {key: {"lighthouse"} for key in
                                             ("placements", "summaries", "summary vectors", "text uids")}
    engine = _lcm_engine(new_dir, monkeypatch)
    try:
        assert engine._dag.get_node(node_id).summary == "where the tide table is kept"
        engine.on_session_start("lcm", conversation_id="c1")
        expanded = engine.handle_tool_call("lcm_expand", {"node_id": node_id})
        assert "the harbor tide table is in drawer 4" in expanded
    finally:
        engine.shutdown()
        lcm_x_store.unroute(new_dir / "lcm.db")


def test_a_failed_rename_gives_the_lcm_records_back(profile_env, memory_store_sql, monkeypatch):
    from hermes_cli.profile_identity import text_moved_for_rename

    profile_dir = create_profile("harbor", no_alias=True)
    _seed(profile_dir)
    _seed_lcm(profile_dir, memory_store_sql, monkeypatch)
    before = _lcm_owners(memory_store_sql)
    with pytest.raises(OSError, match="directory move failed"):
        with text_moved_for_rename(profile_dir, "harbor", "lighthouse"):
            assert _lcm_owners(memory_store_sql)["placements"] == {"lighthouse"}
            raise OSError("directory move failed")

    assert _lcm_owners(memory_store_sql) == before == {key: {"harbor"} for key in before}
    assert set(_agents(memory_store_sql)) == {"harbor"}
