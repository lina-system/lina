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
