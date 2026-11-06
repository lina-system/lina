"""LCM-X on the memory store (lina#16): a routed database belongs to the profile of the home that owns it, wherever
``LCM_DATABASE_PATH`` puts it (Codex P1 on #242). Real Postgres (tests/_fixtures/memory_store.py)."""

from __future__ import annotations

import importlib
import importlib.util
import sys
from pathlib import Path

from hermes_state_store import TEXT_FIELDS, MessageStore
from plugins.context_engine import lcm_x_store

PLUGIN = Path(__file__).resolve().parents[3] / "plugins" / "context_engine" / "lcm-x"
PACKAGE = "lina_lcm_x_profile_agent"


def _lcm(module: str):
    if PACKAGE not in sys.modules:
        spec = importlib.util.spec_from_file_location(PACKAGE, PLUGIN / "__init__.py",
                                                      submodule_search_locations=[str(PLUGIN)])
        package = importlib.util.module_from_spec(spec)
        package.__path__ = [str(PLUGIN)]
        sys.modules[PACKAGE] = package
    return importlib.import_module(f"{PACKAGE}.{module}")


def _home(root: Path, profile: str | None) -> Path:
    home = root / "profiles" / profile if profile else root
    home.mkdir(parents=True, exist_ok=True)
    (home / "config.yaml").write_text("lcm:\n  store_backend: postgres\n")
    return home


def _engine(home: Path, monkeypatch, database: Path | None = None, running_home: Path | None = None):
    # The running home is where the engine reads lcm.store_backend; one process serves several profiles' engines.
    monkeypatch.setenv("HERMES_HOME", str(running_home or home))
    config = _lcm("config").LCMConfig(embeddings_enabled=False, temporal_rollups_enabled=False,
                                      database_path=str(database) if database else "")
    return _lcm("engine").LCMEngine(config=config, hermes_home=str(home))


def _append(engine, session: str, messages: list[dict]) -> None:
    try:
        engine._store.append_batch(session, messages)
    finally:
        db = engine._store.db_path
        engine.shutdown()
        lcm_x_store.unroute(db)


def test_profiles_with_their_own_lcm_database_paths_keep_separate_records(tmp_path, monkeypatch, memory_store_sql):
    root = tmp_path / "hermes"
    elsewhere = tmp_path / "lcm-databases"
    elsewhere.mkdir()
    _home(root, None)
    for profile in ("alpha", "beta"):
        _append(_engine(_home(root, profile), monkeypatch, elsewhere / f"{profile}.db", running_home=root), "s",
                [{"role": "user", "content": f"{profile} keeps the spare key under the mat"}])

    assert sorted(memory_store_sql("SELECT agent_id, count(*) FROM ctx.lcm_messages GROUP BY agent_id")) == [
        ("alpha", 1), ("beta", 1)]
    engine = _engine(root / "profiles" / "alpha", monkeypatch, elsewhere / "alpha.db", running_home=root)
    try:
        assert [m["content"] for m in engine._store.get_session_messages("s")] == [
            "alpha keeps the spare key under the mat"]
    finally:
        engine.shutdown()
        lcm_x_store.unroute(elsewhere / "alpha.db")


def test_a_profile_with_its_own_lcm_database_path_finds_the_text_hermes_wrote_for_it(tmp_path, monkeypatch,
                                                                                    memory_store_sql):
    root = tmp_path / "hermes"
    _home(root, None)
    home = _home(root, "alpha")
    text = MessageStore("alpha")
    try:
        with text.write():
            uid = text.put_text(message_uid="turn-1", values=("where is the spare key",) + (None,) * (len(TEXT_FIELDS) - 1),
                                role="user", tool_name=None, tool_call_id=None, timestamp=None, session_id="s",
                                seq_in_session=None, meta={"message_uid": "turn-1"})
    finally:
        text.close()

    _append(_engine(home, monkeypatch, tmp_path / "elsewhere.db", running_home=root), "s",
            [{"role": "user", "content": "where is the spare key", "message_uid": "turn-1"}])

    assert uid == "agent:alpha:turn-1"
    assert memory_store_sql("SELECT agent_id, text_uid FROM ctx.lcm_messages") == [("alpha", uid)]
    assert memory_store_sql("SELECT uid FROM memory.messages") == [(uid,)]


def test_the_default_lcm_database_of_a_profile_and_of_the_default_home_keep_their_agents(tmp_path, monkeypatch,
                                                                                        memory_store_sql):
    root = tmp_path / "hermes"
    _append(_engine(_home(root, "gamma"), monkeypatch), "s", [{"role": "user", "content": "gamma's line"}])
    _append(_engine(_home(root, None), monkeypatch), "s", [{"role": "user", "content": "the default home's line"}])

    assert sorted(memory_store_sql("SELECT agent_id FROM ctx.lcm_messages")) == [("default",), ("gamma",)]
    assert not (root / "lcm.db").exists() and not (root / "profiles" / "gamma" / "lcm.db").exists()
