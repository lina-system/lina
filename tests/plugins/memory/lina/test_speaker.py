"""lina#324: every remembered item says who said it. Turns run through the real agent and lina-memory; the gateway's
notes reach the agent the way ``_prepare_inbound_message_text`` hands them over (``gateway/inbound_notes.py``)."""

from __future__ import annotations

import json
from types import SimpleNamespace

import pytest

from gateway import inbound_notes
from hermes_cli.store_migrations import MIGRATIONS_DIR
from plugins.memory.lina.identity import derived_id
from plugins.memory.lina.search import recall_line
from tests.plugins.memory.lina.harness import drain, make_agent, run_turn, write_home

QUOTED = "Your invoice total is 4200 won, due Friday."
SCREEN = "A receipt from Cedar Market listing three groceries"
OWNER = "Can you check the total on this one?"
SESSION = "speaker-1"


@pytest.fixture
def home(tmp_path, monkeypatch, lina_env):
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def _gateway_turn() -> tuple[str, dict]:
    """The text the model reads and the user row's display metadata, for a reply to the agent's message with a
    screenshot, built the way the gateway builds them."""
    event = SimpleNamespace()
    notes = inbound_notes.start(event, OWNER)
    text = notes.step("image_reading", OWNER, f"[The user sent an image~ Here's what I can see:\n{SCREEN}]\n\n{OWNER}",
                      quoted_speaker="agent")
    text = notes.step("reply_quote", text, f'[Replying to your previous message: "{QUOTED}"]\n\n{text}',
                      quoted_speaker="agent", quoted_ref="41")
    assert inbound_notes.persist_text(event, text) == OWNER
    return text, inbound_notes.display_metadata(event)


def _search(agent, **args) -> list[dict]:
    provider = next(p for p in agent._memory_manager.providers if p.name == "lina-memory")
    return json.loads(provider.handle_tool_call("memory_search", args))["results"]


def _two_turns(home):
    from hermes_state import SessionDB
    db = SessionDB(home / "state.db")
    agent = make_agent(SESSION, db)
    run_turn(agent, "How much is my invoice?", QUOTED)
    composed, metadata = _gateway_turn()
    run_turn(agent, composed, "Checked: it matches.", db.get_messages_as_conversation(SESSION),
             persist_user_message=OWNER, persist_user_display_metadata=metadata)
    drain(agent)
    return agent, db, composed


def test_turn_rows_store_the_speaker_their_route_gives(home, memory_store_sql):
    agent, db, _ = _two_turns(home)
    try:
        rows = memory_store_sql("SELECT role, speaker, speaker_ref, content FROM memory.messages"
                                " WHERE source = 'agent' ORDER BY message_id")
    finally:
        agent.shutdown_memory_provider()
        db.close()

    assert rows == [("user", "owner", "telegram:1001", "How much is my invoice?"),
                    ("assistant", "agent", "agent:default", QUOTED),
                    ("user", "owner", "telegram:1001", OWNER),
                    ("assistant", "agent", "agent:default", "Checked: it matches.")]


def test_gateway_notes_are_parts_of_the_owner_message_never_its_text(home, memory_store_sql):
    agent, db, composed = _two_turns(home)
    try:
        sent = agent.client.chat.completions.create.call_args.kwargs["messages"]
        parts = memory_store_sql("SELECT m.content, p.kind, p.speaker, p.quoted_speaker, p.content"
                                 " FROM memory.message_parts p JOIN memory.messages m USING (message_id)"
                                 " ORDER BY p.ordinal")
        placements = db._conn.execute("SELECT display_metadata FROM transcript_rows WHERE role = 'user'").fetchall()
    finally:
        agent.shutdown_memory_provider()
        db.close()

    # The model read the notes where the gateway put them; memory holds them beside the owner's words.
    assert [m["content"] for m in sent if m["role"] == "user"][-1] == composed
    assert [(kind, speaker, quoted) for _, kind, speaker, quoted, _ in parts] == [
        ("image_reading", "gateway", "agent"), ("reply_quote", "gateway", "agent")]
    assert {owner for owner, *_ in parts} == {OWNER}
    assert SCREEN in parts[0][4] and QUOTED in parts[1][4]
    assert not any(SCREEN in (meta or "") or "gateway_notes" in (meta or "") for (meta,) in placements)


def test_honcho_gets_the_owner_words_as_theirs_and_notes_on_the_agent_side(home, lina_env):
    agent, db, _ = _two_turns(home)
    agent.shutdown_memory_provider()
    db.close()

    owner_side = [m for m in lina_env.written if m["peer"] != "lina-bot"]
    notes = [m for m in lina_env.written if (m["metadata"] or {}).get("lina_speaker") == "gateway"]
    assert [m["content"] for m in owner_side] == ["How much is my invoice?", OWNER]
    assert {m["metadata"]["lina_speaker"] for m in owner_side} == {"owner"}
    # A screenshot description or a quote is never the owner's words, so no conclusion about them can rest on it.
    assert not any(SCREEN in m["content"] or QUOTED in m["content"] for m in owner_side)
    assert [m["metadata"]["lina_note"] for m in notes] == ["image_reading", "reply_quote"]
    assert {m["peer"] for m in notes} == {"lina-bot"}


def test_a_quoted_agent_message_is_recalled_as_the_agent_and_notes_never_as_the_owner(home):
    agent, db, _ = _two_turns(home)
    try:
        quoted = _search(agent, query="4200 won", sources=["agent"])
        screen = _search(agent, query="Cedar Market", sources=["agent"])
        owner_words_screen = _search(agent, query="Cedar Market", sources=["agent"], speaker=["owner"])
        owner_words = _search(agent, query="check the total", sources=["agent"], speaker=["owner"])
    finally:
        agent.shutdown_memory_provider()
        db.close()

    assert {hit["speaker"] for hit in quoted} == {"agent", "gateway"}
    assert [hit["note"]["quotes"] for hit in quoted if hit.get("note")] == ["agent"]
    assert [(hit["speaker"], hit["note"]["kind"]) for hit in screen] == [("gateway", "image_reading")]
    assert owner_words_screen == []
    assert [(hit["speaker"], hit["author"]) for hit in owner_words] == [("owner", "telegram:1001")]
    labels = {hit["ref"].split(":")[0]: recall_line(hit) for hit in quoted}
    assert " (agent agent:default) " in labels["msg"]
    assert " (gateway note (reply_quote, quoting agent)) " in labels["note"]


def test_the_next_turn_replays_the_bytes_the_model_read(home):
    agent, db, composed = _two_turns(home)
    try:
        run_turn(agent, "Thanks.", "You are welcome.", db.get_messages_as_conversation(SESSION))
        replayed = agent.client.chat.completions.create.call_args.kwargs["messages"]
    finally:
        agent.shutdown_memory_provider()
        db.close()
    assert [m["content"] for m in replayed if m["role"] == "user"][-2] == composed


def _legacy_rows(memory_store_sql):
    """Rows as a store held them before migration 0013: no speaker."""
    memory_store_sql("ALTER TABLE memory.messages DISABLE TRIGGER messages_speaker_by_rule;"
                     " ALTER TABLE memory.messages ALTER COLUMN speaker DROP NOT NULL")
    rows = [("agent:p:u1", "agent", "p", "user", None, None, "{}"),
            ("agent:p:a1", "agent", "p", "assistant", None, None, "{}"),
            ("agent:p:t1", "agent", "p", "tool", "web_search", None, "{}"),
            ("agent:p:s1", "agent", "p", "user", None, None, '{"_compressed_summary": true}'),
            ("messages:x1", "messages", None, "user", None, "kakao:k-1", "{}"),
            ("heart:p:h1", "heart", "p", None, None, None, '{"evidence": ["agent:p:u1", "agent:p:a1"]}'),
            ("honcho:w:m1", "agent", "w", "user", None, None, "{}")]
    for uid, source, agent_id, role, tool, author, native in rows:
        memory_store_sql("INSERT INTO memory.messages (uid, source, agent_id, role, tool_name, author, native,"
                         " content, content_sha256) VALUES (%s, %s, %s, %s, %s, %s, %s::jsonb, 'x', 'x')",
                         (uid, source, agent_id, role, tool, author, native))


def test_the_migration_backfills_old_rows_by_stored_fields_only_and_runs_again(memory_store_sql):
    body = (MIGRATIONS_DIR / "0013_speaker.sql").read_text(encoding="utf-8")
    _legacy_rows(memory_store_sql)
    try:
        memory_store_sql(body)
        first = memory_store_sql("SELECT uid, speaker, speaker_ref FROM memory.messages ORDER BY uid")
        memory_store_sql(body)
        again = memory_store_sql("SELECT uid, speaker, speaker_ref FROM memory.messages ORDER BY uid")
        derived = memory_store_sql("SELECT uid, source_speakers FROM memory.message_source_speakers")
        authors = memory_store_sql("SELECT source, key, identity_id FROM memory.identity_keys")
    finally:
        memory_store_sql("ALTER TABLE memory.messages ENABLE TRIGGER messages_speaker_by_rule")

    assert first == again == [
        ("agent:p:a1", "agent", "agent:p"), ("agent:p:s1", "agent", "agent:p"), ("agent:p:t1", "outside", "tool:web_search"),
        ("agent:p:u1", "unknown", None), ("heart:p:h1", "agent", "agent:p"), ("honcho:w:m1", "unknown", None),
        ("messages:x1", "outside", "kakao:k-1")]
    assert derived == [("heart:p:h1", ["agent", "unknown"])]
    # An external author is a person of the registry, under the id lina-memory derives for its handle.
    assert authors == [("messenger_author", "kakao:k-1", derived_id("person", "messenger_author", "kakao:k-1"))]


def test_a_row_written_without_a_speaker_gets_the_rule_kind(memory_store_sql):
    memory_store_sql("INSERT INTO memory.messages (uid, source, agent_id, role, content, author, content_sha256)"
                     " VALUES ('messages:x2', 'messages', NULL, 'user', 'hi', 'line:u-5', 'x'),"
                     " ('honcho:w:m2', 'agent', 'w', 'user', 'hi', NULL, 'x')")
    assert memory_store_sql("SELECT uid, speaker, speaker_ref FROM memory.messages ORDER BY uid") == [
        ("honcho:w:m2", "unknown", None), ("messages:x2", "outside", "line:u-5")]


def test_a_summary_keeps_the_speakers_of_every_message_under_it(memory_store_sql):
    for uid, role in (("agent:p:u9", "user"), ("agent:p:a9", "assistant")):
        memory_store_sql("INSERT INTO memory.messages (uid, source, agent_id, role, content, content_sha256)"
                         " VALUES (%s, 'agent', 'p', %s, 'x', 'x')", (uid, role))
    memory_store_sql("UPDATE memory.messages SET speaker = 'owner' WHERE uid = 'agent:p:u9'")
    for store_id, uid in ((1, "agent:p:u9"), (2, "agent:p:a9")):
        memory_store_sql("INSERT INTO ctx.lcm_messages (agent_id, store_id, session_id, text_uid, content_sha256,"
                         " timestamp) VALUES ('p', %s, 's', %s, 'x', 0)", (store_id, uid))
    for node_id, source_ids, source_type in ((10, "[1]", "messages"), (11, "[2]", "messages"), (12, "[10, 11]", "nodes")):
        memory_store_sql("INSERT INTO ctx.summary_nodes (agent_id, node_id, session_id, depth, summary, source_ids,"
                         " source_type, created_at) VALUES ('p', %s, 's', %s, 'x', %s, %s, 0)",
                         (node_id, 0 if source_type == "messages" else 1, source_ids, source_type))

    assert memory_store_sql("SELECT node_id, source_speakers FROM ctx.summary_speakers ORDER BY node_id") == [
        (10, ["owner"]), (11, ["agent"]), (12, ["agent", "owner"])]
