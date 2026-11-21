"""Collector canaries through live agent tools, the persisted vector index and the session card."""

import argparse
import json
from datetime import date

import pytest

from hermes_cli.brain import extract, page, people
from hermes_cli.brain.resolver import DEFAULT_MAPPING, mapping_from
from hermes_cli.brain.scan import scan
from hermes_cli.brain.store import owner_connection, sync
from hermes_cli.external_messages import ingest
from hermes_cli.sources.spool import message_uid
from tests.plugins.memory.lina.harness import make_agent, write_home
from utils import atomic_roundtrip_yaml_update


SESSION = "20261101_120000_sources"
CANARY = {"mail": "cobaltfinch", "slack": "violetwren", "kakao": "bronzeibis"}
LINK = {"mail": "display_url", "slack": "permalink"}


def switch(home, platform, value):
    atomic_roundtrip_yaml_update(home / "config.yaml", f"memory.external_messages.sources.{platform}.enabled", value)


@pytest.fixture
def session(tmp_path, monkeypatch, lina_env, memory_store_sql):
    from hermes_state import SessionDB

    home = write_home(tmp_path / "home")
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("LINA_HOME", str(home))
    (home / "memories").mkdir()
    (home / "memories" / "MEMORY.md").write_text("Fixed note about the garden.", encoding="utf-8")
    for platform in LINK:
        switch(home, platform, True)
    db = SessionDB(home / "state.db")
    agent = make_agent(SESSION, db)
    provider = next(p for p in agent._memory_manager.providers if p.name == "lina-memory")
    person = provider._session_person()
    assert person
    wiki = tmp_path / "wiki"
    wiki.mkdir()
    (wiki / "finch.md").write_text(page.render(page.new_page(
        "person", "Finch", "Gardens at sunrise.", extra={"person_id": person})), encoding="utf-8")
    with owner_connection() as conn:
        sync(conn, scan(wiki, mapping_from(DEFAULT_MAPPING)))
    yield agent, provider, home
    agent._memory_manager.shutdown_all()
    db.close()


def ingest_messages(home, platforms):
    messages = []
    refs = {}
    for platform in platforms:
        word = CANARY[platform]
        native_id = f"record-{platform}"
        uid = message_uid(platform, "room", native_id)
        refs[platform] = "msg:messages:" + uid
        message = {"source": platform, "uid": uid, "room_id": "room", "native_id": native_id, "author_id": "sender",
                   "text": f"aviary {word}", "ts": "2026-11-01T12:00:00Z", "ingest_path": "live"}
        if platform in LINK:
            message[LINK[platform]] = f"https://example.test/{platform}?token=fixture-secret-value"
        messages.append(message)
    spool = home / "events.jsonl"
    spool.write_text(json.dumps({"version": 1, "event_id": "c" * 32, "messages": messages}) + "\n", encoding="utf-8")
    assert ingest(spool).inserted == len(messages)
    return refs


def seed(home):
    refs = ingest_messages(home, CANARY)
    records = [extract.Record("Finch", date(2026, 11, 1), f"derived {CANARY[kind]}", ref) for kind, ref in refs.items()]
    with owner_connection() as conn:
        written = extract.write_records(conn, records, home)
    assert written == {"finch": records}
    return refs


def call(agent, name, **params):
    return json.loads(agent._memory_manager.handle_tool_call(name, params, session_id=SESSION))


def cli(capsys, *argv):
    from hermes_cli.subcommands.messages import build_messages_parser

    parser = argparse.ArgumentParser()
    build_messages_parser(parser.add_subparsers())
    args = parser.parse_args(["messages", *argv])
    capsys.readouterr()
    args.func(args)
    return json.loads(capsys.readouterr().out)


@pytest.mark.parametrize("platform,other", [("mail", "slack"), ("slack", "mail")])
def test_live_reads_hide_only_the_disabled_platform(session, memory_store_sql, platform, other, capsys):
    agent, provider, home = session
    refs = seed(home)
    ref, word = refs[platform], CANARY[platform]
    uid = ref.removeprefix("msg:")
    # Old collector rows can predate native.platform; use the channel and author on those rows.
    memory_store_sql("UPDATE memory.messages SET native = native - 'platform' WHERE uid = %s", (uid,))
    store = provider._message_store()
    manager = agent._memory_manager
    with owner_connection() as conn:
        for visible in (True, False, True):
            switch(home, platform, visible)
            for tool, arg in (("brain_search", "kinds"), ("memory_search", "sources")):
                hits = call(agent, tool, query="aviary", **{arg: ["messages"]})["results"]
                by_ref = {h["ref"]: h for h in hits}
                assert (ref in by_ref) == visible
                assert refs[other] in by_ref and refs["kakao"] in by_ref
                if visible:
                    hit = by_ref[ref]
                    assert (hit["speaker"], hit["grade"], hit["quote_as_external"]) == ("outside", "external", True)
                    assert hit["native_id"] == f"record-{platform}"
                    assert f"https://example.test/{platform}" in hit[LINK[platform]]
                    assert "fixture-secret-value" not in hit[LINK[platform]]
            for tool in ("brain_get", "memory_open"):
                opened = call(agent, tool, ref=ref)
                if visible:
                    assert word in opened["text"] and opened[LINK[platform]] == hit[LINK[platform]]
                else:
                    assert "error" in opened
            for criteria in ({"sender": [f"{platform}:sender"]}, {"person": f"messenger_author:{platform}:sender"}):
                hits = call(agent, "brain_search", query=word, kinds=["messages"], **criteria)["results"]
                assert [h["ref"] for h in hits] == ([ref] if visible else [])
            cli_hits = cli(capsys, "search", word, "--sender", f"{platform}:sender")["results"]
            assert [h["ref"] for h in cli_hits] == ([ref] if visible else [])
            if visible:
                assert word in cli(capsys, "open", ref)["text"]
            else:
                with pytest.raises(SystemExit):
                    cli(capsys, "open", ref)
            for text in (manager.handle_tool_call("memory_recall", {"query": word}, session_id=SESSION),
                         manager.prefetch_all(word, session_id=SESSION)):
                assert (word in text) == visible
                if visible:
                    before, quoted = text.split("<external-data>", 1)
                    body, after = quoted.split("</external-data>", 1)
                    assert word in body and word not in before + after
            assert (uid in store.texts([uid])) == visible
            assert (ref in extract.pending(conn)) == visible
            assert (extract.resolve(conn, ref) is not None) == visible
            page = call(agent, "brain_get", ref="page:finch")
            assert page["timeline_total"] == (3 if visible else 2)
            assert (ref in [e["source"] for e in page["timeline"]]) == visible
            assert (word in str(people.page_of(conn, provider._session_person()).timeline)) == visible
        atomic_roundtrip_yaml_update(home / "config.yaml", "memory.external_messages.enabled", False)
        assert not extract.pending(conn) and extract.resolve(conn, ref) is None


def test_mixed_cached_vector_excludes_all_contributors_while_one_is_hidden(session, memory_store_sql):
    _, provider, home = session
    refs = seed(home)
    ids = [row[0] for row in memory_store_sql("SELECT message_id FROM memory.messages WHERE uid = ANY(%s)",
                                               ([refs[k][4:] for k in LINK],))]
    vector = [1.] + [0.] * 1023
    chunk = memory_store_sql("INSERT INTO vec.chunks(source, agent_id, message_ids, text) VALUES "
                             "('messages', '*', %s, 'shared vector') RETURNING chunk_id", (ids,))[0][0]
    memory_store_sql("INSERT INTO vec.embeddings(kind, item_id, agent_id, model, dim, embedding) VALUES "
                     "('chunk', %s, '*', 'aviary', 1024, %s::vector)", (chunk, str(vector)))
    store = provider._message_store()
    args = {"model": "aviary", "sources": ["messages"]}
    for platform in LINK:
        for visible in (True, False, True):
            switch(home, platform, visible)
            assert store.has_memory_vectors(**args) == visible
            assert {"msg:" + r.uid for r in store.search_memory_vectors(vector, **args)} == (
                {refs[k] for k in LINK} if visible else set())


def test_reembed_picks_up_an_older_source_row_after_reenable(session):
    from hermes_cli.store_reembed import reembed_source, rows_left

    _, _, home = session
    seed(home)
    switch(home, "mail", False)
    calls = []

    def embed(texts):
        calls.extend(texts)
        return [[1.] + [0.] * 1023 for _ in texts]

    with owner_connection() as conn:
        conn.autocommit = True
        args = {"source": "messages", "model": "aviary", "batch_size": 1}
        reembed_source(conn, embed, **args)
        assert set(calls) == {f"aviary {CANARY[k]}" for k in ("slack", "kakao")}
        switch(home, "mail", True)
        assert rows_left(conn, "messages", "aviary") == 1
        calls.clear()
        reembed_source(conn, embed, **args)
        assert calls == [f"aviary {CANARY['mail']}"]
        assert rows_left(conn, "messages", "aviary") == 0


def test_rebuilt_session_prompt_and_notes_are_byte_identical_after_collection_and_extraction(session):
    from agent.prompt_builder import NOTES_END, NOTES_HEADING
    from agent.system_prompt import build_system_prompt
    from tests.hermes_cli.test_brain_extract import FakeExtractor

    agent, _, home = session

    def snapshot():
        prompt = build_system_prompt(agent).encode()
        start = prompt.index(NOTES_HEADING.encode())
        end = prompt.index(NOTES_END.encode(), start) + len(NOTES_END.encode())
        notes = prompt[start:end]
        assert b"Fixed note about the garden." in notes and b"Gardens at sunrise." in notes
        return prompt, notes

    before = snapshot()
    refs = ingest_messages(home, LINK)
    answers = {ref: {"who": "Finch", "when": "2026-11-01", "what": f"derived {CANARY[kind]}"}
               for kind, ref in refs.items()}
    with owner_connection() as conn:
        result = extract.extract(conn, FakeExtractor(answers), home)
    assert result["lines_by_page"] == {"finch": 2}
    timeline = (home.parent / "wiki" / "finch.md").read_text(encoding="utf-8")
    for kind, ref in refs.items():
        assert f"| external | {ref} | derived {CANARY[kind]}" in timeline
    for enabled in (True, False, True):
        for platform in LINK:
            switch(home, platform, enabled)
        prompt, notes = snapshot()
        assert notes == before[1]
        assert prompt == before[0]


def test_proposals_keep_hiding_with_their_source_after_the_atoms_are_rebuilt(session):
    from collections import Counter

    from hermes_cli.brain.dream.cycle import _write
    from hermes_cli.brain.dream.phases import Atoms, Proposal
    from hermes_cli.brain.dream.proposals import listing

    _, _, home = session
    refs = seed(home)
    claims = tuple((f"derived {CANARY[kind]}", "external", refs[kind]) for kind in LINK)
    with owner_connection() as conn:
        conn.autocommit = True
        _write(conn, [Atoms("finch", claims)], "atoms", "agent", 1, Counter(), [])
        ids = {ref: atom_id for atom_id, ref in conn.execute("SELECT atom_id, source_ref FROM brain.atoms")}
        proposals = [Proposal("new_page", kind, {"type": "concept"}, "external", (f"atom:{ids[refs[kind]]}",), kind)
                     for kind in LINK]
        _write(conn, proposals, "concepts", "agent", 1, Counter(), [])
        _write(conn, [Atoms("finch", claims)], "atoms", "agent", 2, Counter(), [])
        assert not set(ids.values()) & {row[0] for row in conn.execute("SELECT atom_id FROM brain.atoms")}
        assert {row["target"] for row in listing(conn, "agent", None, None)} == set(LINK)
        for platform, other in (("mail", "slack"), ("slack", "mail")):
            switch(home, platform, False)
            assert {row["target"] for row in listing(conn, "agent", None, None)} == {other}
            switch(home, platform, True)
