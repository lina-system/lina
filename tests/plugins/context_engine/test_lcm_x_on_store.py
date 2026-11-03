"""LCM-X on the memory store (lina#16): its stores run on schema ``ctx`` and read message text from
``memory.messages``, through ``plugins/context_engine/lcm_x_store.py``. Real Postgres (tests/_fixtures/memory_store.py)."""

from __future__ import annotations

import importlib
import importlib.util
import sqlite3
import sys
import time
from pathlib import Path

import pytest

import hermes_state_store
from plugins.context_engine import lcm_x_store, lcm_x_store_fts

PLUGIN = Path(__file__).resolve().parents[3] / "plugins" / "context_engine" / "lcm-x"
PACKAGE = "lina_lcm_x_on_store"


def _lcm(module: str):
    if PACKAGE not in sys.modules:
        spec = importlib.util.spec_from_file_location(PACKAGE, PLUGIN / "__init__.py",
                                                      submodule_search_locations=[str(PLUGIN)])
        package = importlib.util.module_from_spec(spec)
        package.__path__ = [str(PLUGIN)]
        sys.modules[PACKAGE] = package
    return importlib.import_module(f"{PACKAGE}.{module}")


@pytest.fixture
def routed(tmp_path):
    """A profile home whose lcm.db is routed to the store; unrouted after the test."""
    paths = []

    def make(profile: str) -> Path:
        db = tmp_path / "profiles" / profile / "lcm.db"
        db.parent.mkdir(parents=True)
        assert lcm_x_store.route(db) == profile
        paths.append(db)
        return db
    yield make
    for db in paths:
        lcm_x_store.unroute(db)


def _stores(db: Path):
    return _lcm("store").MessageStore(db), _lcm("dag").SummaryDAG(db)


def test_korean_two_character_term_recalls_messages_and_summaries(routed):
    db = routed("ko")
    store, dag = _stores(db)
    try:
        ids = store.append_batch("s1", [{"role": "user", "content": "어제 회의록을 정리했다"},
                                        {"role": "assistant", "content": "deploy notes are ready"}])
        node = _lcm("dag").SummaryNode(session_id="s1", summary="회의록 요약: 출시 일정", source_ids=ids,
                                       token_count=5, created_at=time.time())
        node_id = dag.add_node(node)

        assert [row["store_id"] for row in store.search("회의", limit=5)] == [ids[0]]
        assert [row["store_id"] for row in store.search("deploy", limit=5)] == [ids[1]]
        assert [n.node_id for n in dag.search("회의", limit=5)] == [node_id]
        assert not db.exists()
    finally:
        store.close()
        dag.close()


def test_a_hermes_turn_is_not_stored_twice(routed, memory_store_sql):
    db = routed("p1")
    text = hermes_state_store.MessageStore("p1")
    values = ("회의는 3시에", None) + (None,) * (len(hermes_state_store.TEXT_FIELDS) - 2)
    with text.write():
        uid = text.put_text(message_uid="m-1", values=values, role="user", tool_name=None, tool_call_id=None,
                            timestamp=time.time(), session_id="hermes-s", seq_in_session=1, meta={})
    text.close()
    store, dag = _stores(db)
    try:
        [store_id] = store.append_batch("s1", [{"role": "user", "content": "회의는 3시에", "message_uid": "m-1"}])
        assert store.get(store_id)["content"] == "회의는 3시에"
        rows = memory_store_sql("SELECT uid FROM memory.messages WHERE agent_id = 'p1'")
        assert rows == [(uid,)]
        assert memory_store_sql("SELECT text_uid FROM ctx.lcm_messages WHERE store_id = %s", (store_id,)) == [(uid,)]

        [other] = store.append_batch("s1", [{"role": "assistant", "content": "알겠습니다", "message_uid": "m-2"}])
        assert store.get(other)["content"] == "알겠습니다"
        assert len(memory_store_sql("SELECT uid FROM memory.messages WHERE agent_id = 'p1'")) == 2
    finally:
        store.close()
        dag.close()


def test_summary_vectors_live_in_vec_embeddings(routed, memory_store_sql):
    from array import array
    db = routed("vec")
    store, dag = _stores(db)
    conn = lcm_x_store.backend_for(db)()
    try:
        node_id = dag.add_node(_lcm("dag").SummaryNode(session_id="s1", summary="회의 요약", created_at=time.time()))
        conn.execute("INSERT INTO lcm_embedding_profile(identity_hash, provider, model_name, dim) VALUES (?, ?, ?, ?)",
                     ("h1", "openai", "qwen3-embedding-8b", 1024))
        blob = array("f", [0.5] * 1024).tobytes()
        conn.execute("INSERT INTO lcm_embedding_vectors(embedded_id, identity_hash, vec) VALUES(?, ?, ?)",
                     (str(node_id), "h1", blob))
        conn.commit()
        assert memory_store_sql("SELECT kind, item_id, agent_id, model FROM vec.embeddings") == [
            ("summary", node_id, "vec", "qwen3-embedding-8b")]
        assert conn.execute("SELECT vec FROM lcm_embedding_vectors WHERE embedded_id = ?",
                            (str(node_id),)).fetchone()[0] == blob
        for width in (8, 1536):
            with pytest.raises(sqlite3.IntegrityError, match="1024 wide"):
                conn.execute("INSERT INTO lcm_embedding_vectors(embedded_id, identity_hash, vec) VALUES(?, ?, ?)",
                             (str(node_id), "h1", array("f", [0.5] * width).tobytes()))
            conn.rollback()
        # The store's vector column is fixed by its migrations: a vector of another type is refused too.
        conn.execute("INSERT INTO lcm_embedding_profile(identity_hash, provider, model_name, dim, dtype) "
                     "VALUES (?, ?, ?, ?, ?)", ("h8", "openai", "qwen3-embedding-8b", 1024, "int8"))
        conn.commit()
        with pytest.raises(sqlite3.IntegrityError, match="float32"):
            conn.execute("INSERT INTO lcm_embedding_vectors(embedded_id, identity_hash, vec) VALUES(?, ?, ?)",
                         (str(node_id), "h8", bytes(1024)))
        conn.rollback()
        conn.execute("DELETE FROM lcm_embedding_vectors WHERE embedded_id = ? AND identity_hash = ?",
                     (str(node_id), "h1"))
        conn.commit()
        assert memory_store_sql("SELECT count(*) FROM vec.embeddings") == [(0,)]
    finally:
        conn.close()
        store.close()
        dag.close()


def test_chunk_vectors_live_in_vec_chunks_and_embeddings(routed, memory_store_sql):
    from array import array
    db = routed("chunks")
    store, dag = _stores(db)
    conn = lcm_x_store.backend_for(db)()
    try:
        [store_id] = store.append_batch("s1", [{"role": "user", "content": "첫 문장. 두 번째 문장."}])
        conn.execute("INSERT INTO lcm_embedding_profile(identity_hash, provider, model_name, dim) VALUES (?, ?, ?, ?)",
                     ("h1", "openai", "qwen3-embedding-8b", 1024))
        blob = array("f", [0.5] * 1024).tobytes()
        conn.execute("INSERT INTO lcm_chunk_vectors(chunk_id, identity_hash, vec) VALUES(?, ?, ?)",
                     (f"{store_id}:1", "h1", blob))
        conn.execute("INSERT INTO lcm_chunk_meta(chunk_id, identity_hash, store_id, chunk_index, char_start, char_end, "
                     "token_estimate, embedded_at, archived) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)",
                     (f"{store_id}:1", "h1", store_id, 1, 6, 14, 3, "now"))
        conn.commit()
        [(message_id,)] = memory_store_sql("SELECT message_id FROM memory.messages WHERE agent_id = 'chunks'")
        assert memory_store_sql("SELECT source, message_ids, chunk_index, text FROM vec.chunks") == [
            ("lcm-x", [message_id], 1, "두 번째 문장.")]
        assert memory_store_sql("SELECT kind, model FROM vec.embeddings") == [("chunk", "qwen3-embedding-8b")]
        assert conn.execute("SELECT chunk_id, vec FROM lcm_chunk_vectors").fetchall() == [(f"{store_id}:1", blob)]
        conn.execute("DELETE FROM lcm_chunk_vectors WHERE chunk_id = ? AND identity_hash = ?", (f"{store_id}:1", "h1"))
        conn.commit()
        assert memory_store_sql("SELECT count(*) FROM vec.embeddings") == [(0,)]
    finally:
        conn.close()
        store.close()
        dag.close()


@pytest.mark.parametrize("backend", ["auto", "postgres"])
def test_store_backend_routes_a_fresh_home_and_keeps_an_existing_lcm_db(tmp_path, backend):
    session = _lcm("sqlite_session")
    fresh = tmp_path / "profiles" / "fresh" / "lcm.db"
    old = tmp_path / "profiles" / "old" / "lcm.db"
    old.parent.mkdir(parents=True)
    old.write_bytes(b"SQLite format 3\x00")
    try:
        assert session.route_to_host_store(fresh, "sqlite") is False and lcm_x_store.backend_for(fresh) is None
        assert session.route_to_host_store(fresh, backend) is True and lcm_x_store.backend_for(fresh) is not None
        assert session.route_to_host_store(old, backend) is False and lcm_x_store.backend_for(old) is None
        assert old.read_bytes() == b"SQLite format 3\x00"
    finally:
        lcm_x_store.unroute(fresh)


def test_each_profile_sees_only_its_own_rows(routed):
    first, second = routed("alpha"), routed("beta")
    store_a, dag_a = _stores(first)
    store_b, dag_b = _stores(second)
    try:
        [mine] = store_a.append_batch("s1", [{"role": "user", "content": "alpha only"}])
        assert store_b.get(mine) is None
        assert store_b.search("alpha", limit=5) == []
        assert store_a.get(mine)["content"] == "alpha only"
    finally:
        for closable in (store_a, dag_a, store_b, dag_b):
            closable.close()


def test_entity_names_fold_case_as_python_does(routed, tmp_path):
    """``Straße`` folds to ``strasse`` (str.casefold), which SQL's lower() does not do: the store matches entity
    names by LCM-X's own normalisation, applied before the query."""
    db = routed("entities")
    config = _lcm("config").LCMConfig(database_path=str(db), embeddings_enabled=False, assertions_enabled=True)
    config.entities_enabled = True
    engine = _lcm("engine").LCMEngine(config=config, hermes_home=str(tmp_path / "home"))
    try:
        engine.on_session_start("s1", conversation_id="c1")
        sid = engine._store.append("s1", {"role": "user", "content": "Straße opened the depot."})
        snapshot = engine._assertions.snapshot_source(sid)
        engine._assertions.publish_source(snapshot, [_lcm("assertion_store").AssertionCandidate(
            0, len(snapshot.content), "person:Straße", "note", value_text=snapshot.content)])
        facts = _lcm("memory_tools")._assertions(engine._assertions, ["strasse"], 10)["facts"]
        assert [fact["subject_key"] for fact in facts] == ["person:Straße"]
    finally:
        engine.shutdown()


@pytest.mark.parametrize("query", [
    "alpha OR", "alpha OR beta", "NOT alpha", "alpha NOT beta", '"alpha beta', "(alpha", "part-42", "api:v2",
    "alpha (beta OR gamma)", "alpha AND (beta OR gamma)", "content:alpha", "NEAR(a b, 3)", "NEAR(a, b)",
    "*abc", "a+", "a,b", "회의 계획", "", "x* ( a:b AND", 'content: ( x-y {x}: OR',
])
def test_a_match_query_fts5_refuses_raises_as_fts5_does(query):
    """LCM-X retries or falls back to LIKE on FTS5's MATCH errors: the store raises the same kind of error for the
    same query (a syntax error, an unterminated string, an unknown column, a special query), or none."""
    def outcome(check):
        try:
            check()
        except sqlite3.OperationalError as exc:
            return str(exc).split(" near ")[0]
        return None
    sqlite_fts = sqlite3.connect(":memory:")
    sqlite_fts.execute("CREATE VIRTUAL TABLE f USING fts5(content)")
    try:
        expected = outcome(lambda: sqlite_fts.execute("SELECT * FROM f WHERE f MATCH ?", (query,)).fetchall())
    finally:
        sqlite_fts.close()
    assert outcome(lambda: lcm_x_store_fts.fts5_check(query)) == expected


def test_level3_repair_on_the_store_skips_the_file_backup_and_says_so(routed, tmp_path, monkeypatch):
    """The store has no database file to back up: /lcm doctor repair level3 apply repairs each group in its own
    transaction and reports the skipped backup in one line, instead of failing on the missing file."""
    escalation, dag_module, tokens = _lcm("escalation"), _lcm("dag"), _lcm("tokens")
    config = _lcm("config").LCMConfig(database_path=str(routed("repair")), embeddings_enabled=False)
    engine = _lcm("engine").LCMEngine(config=config, hermes_home=str(tmp_path / "home"))
    monkeypatch.setattr(_lcm("level3_repair"), "summarize_with_escalation",
                        lambda *, text, depth, **_: (f"Model summary at d{depth}.", 1))
    try:
        engine.on_session_start("s1", conversation_id="s1")
        rows = [engine._store.append("s1", {"role": "user", "content": f"row {i}"}, token_estimate=4) for i in range(2)]
        fragment = escalation._deterministic_truncate("\n".join(f"[user]: step {i} checked host {i}." for i in range(400)),
                                                       512)
        node = engine._dag.add_node(dag_module.SummaryNode(
            session_id="s1", depth=0, summary=fragment, token_count=tokens.count_tokens(fragment),
            source_token_count=1000, source_ids=rows, source_type="messages", created_at=2.0))
        result = _lcm("command").handle_lcm_command("doctor repair level3 apply", engine)
        summary = engine._dag.connection.execute("SELECT summary FROM summary_nodes WHERE node_id = ?",
                                                 (node,)).fetchone()[0]
    finally:
        engine.shutdown()
    lines = result.splitlines()
    assert "status: ok" in lines and "groups_repaired: 1" in lines
    assert [line for line in lines if line.startswith("backup")] == [
        "backup: skipped — " + _lcm("level3_repair").STORE_BACKUP_SKIPPED]
    assert summary == "Model summary at d0."


def test_doctor_lists_the_tables_the_ctx_schema_has(routed, tmp_path, memory_store_sql):
    """On the store /lcm doctor reports the tables Postgres has in schema ctx (as Postgres's catalog lists them),
    no missing required table, and the database as present though no lcm.db file exists."""
    import json
    db = routed("doctor")
    config = _lcm("config").LCMConfig(database_path=str(db), embeddings_enabled=False)
    engine = _lcm("engine").LCMEngine(config=config, hermes_home=str(tmp_path / "home"))
    try:
        engine.on_session_start("s1", conversation_id="c1")
        report = json.loads(_lcm("tools").lcm_doctor({}, engine=engine))
    finally:
        engine.shutdown()
    [schema] = [check["detail"] for check in report["checks"]
                if isinstance(check.get("detail"), dict) and "existing_tables" in check["detail"]]
    ctx_tables = [name for (name,) in memory_store_sql(
        "SELECT table_name::text FROM information_schema.tables WHERE table_schema = 'ctx' "
        "AND table_name <> 'sqlite_master' ORDER BY 1")]
    assert schema["existing_tables"] == sorted(ctx_tables)
    assert set(schema["required_tables"]) <= set(ctx_tables)
    assert schema["missing_tables"] == []
    [storage] = [check for check in report["checks"] if check["check"] == "sqlite_storage"]
    assert storage["detail"]["database_exists"] is True
    assert not db.exists()


def test_one_store_connection_serves_many_threads_and_closes_cleanly(routed):
    """LCM-X shares one connection between threads (check_same_thread=False) and may close it while another thread
    is mid-statement, as SQLite's connection mutex allows: statements wait their turn and a closed connection
    refuses the next one, never two commands on the wire at once."""
    import threading

    store = _lcm("store").MessageStore(routed("threads"))
    conn = store.connection
    errors: list[BaseException] = []
    started, busy = threading.Barrier(9), threading.Event()

    def work(worker: int) -> None:
        started.wait()
        try:
            for step in range(400):
                conn.execute("INSERT OR REPLACE INTO metadata(key, value) VALUES(?, ?)", (f"w{worker}", str(step)))
                conn.commit()
                conn.execute("SELECT count(*) FROM messages").fetchone()
                if step == 20:
                    busy.set()
        except sqlite3.ProgrammingError:
            pass  # the connection was closed under this thread: what SQLite raises too
        except BaseException as exc:  # every other error is the failure under test
            errors.append(exc)

    threads = [threading.Thread(target=work, args=(i,)) for i in range(8)]
    for thread in threads:
        thread.start()
    started.wait()
    assert busy.wait(timeout=60)
    conn.close()
    for thread in threads:
        thread.join(timeout=60)
    assert errors == []


_SNIPPET_ROWS = [
    "New\ue000York " + "filler " * 60 + "New York office. " + "tail " * 60,
    "Keep vendoring out of hermes-agent: see payload.json, naïve café notes.",
    "query audit notes: vendoring vendoring vendoring vendoring vendoring",
    "First sentence about nothing. Second: the database migration plan is here " + "and more words " * 20,
]


@pytest.mark.parametrize("query", ['"New York"', "vendoring", "payload", "naive", '"database migration plan"', "hermes"])
def test_search_snippets_and_order_match_sqlite_fts5(routed, tmp_path, query):
    """FTS5's unicode61 tokens, bm25 order and snippet() on the store, against the same rows in a SQLite lcm.db."""
    results = []
    for db in (routed("snip"), tmp_path / "sqlite" / "lcm.db"):
        db.parent.mkdir(parents=True, exist_ok=True)
        store = _lcm("store").MessageStore(db)
        try:
            ids = store.append_batch("s1", [{"role": "user", "content": text} for text in _SNIPPET_ROWS])
            rows = store.search(query, session_id="s1", limit=10, sort="relevance")
            results.append([(ids.index(row["store_id"]), row["snippet"]) for row in rows])
        finally:
            store.close()
    on_store, on_sqlite = results
    assert on_sqlite
    assert on_store == on_sqlite
