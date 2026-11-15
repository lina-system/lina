"""lina#22: one ``memory_search`` over this agent's turns, the owner's external messages, Honcho and the LCM-X
summaries, and ``memory_open`` of each hit's ref, through the real agent's memory manager. The store is the test
Postgres, LCM-X is the real engine with a summary added through its DAG, and Honcho is the recording SDK fake."""

from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace
from unittest.mock import MagicMock

import httpx
import pytest

from tests.plugins.memory.lina.harness import drain, make_agent, run_turn, write_home

SESSION = "search-1"
EXTERNAL_TEXT = "orchid delivery tomorrow, 내일 회의실에서 봐요"
OTHER_AGENT_TEXT = "orchid secret of another agent, 회의 비밀"


@pytest.fixture
def home(tmp_path, monkeypatch, lina_env):
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def _insert(memory_store_sql, *, uid, source, agent_id, scope, role, content, author=None, conversation=None,
            ts="2026-11-01T09:00:00+00:00"):
    memory_store_sql(
        "INSERT INTO memory.messages (uid, source, agent_id, scope, role, content, author, conversation_id, ts,"
        " content_sha256) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, 'x')",
        (uid, source, agent_id, scope, role, content, author, conversation, ts))


def _honcho(lina_env):
    """Honcho's SDK answering one message and one conclusion, for search and for open."""
    message = SimpleNamespace(id="hm-1", content="orchid order placed with the florist", peer_id="1001",
                              session_id="hs-1", created_at=datetime(2026, 10, 30, 8, 0, tzinfo=UTC))
    conclusion = SimpleNamespace(id="hc-1", content="The person likes orchids", observer_id="lina-bot",
                                 observed_id="1001", session_id="hs-1", created_at=datetime(2026, 10, 31, tzinfo=UTC))
    lina_env.search = MagicMock(return_value=[message])
    lina_env.session("hs-1").get_message.return_value = message
    for peer in list(lina_env._peers.values()):
        peer.conclusions_of.return_value.query.return_value = [conclusion]
        peer.conclusions_of.return_value.get.return_value = conclusion
    return message, conclusion


def _seed_summary(agent):
    from importlib import import_module
    SummaryNode = import_module("plugins.context_engine.lcm-x.dag").SummaryNode
    engine = agent.context_compressor
    return engine._dag.add_node(SummaryNode(
        session_id=SESSION, depth=0, summary="Summary: the orchid delivery was arranged for tomorrow.",
        token_count=12, source_token_count=40, source_ids=[1, 2], source_type="messages",
        expand_hint="orchid delivery", earliest_at=1793520000.0, latest_at=1793520060.0))


@pytest.fixture
def seeded(home, lina_env, memory_store_sql):
    from hermes_state import SessionDB

    agent = make_agent(SESSION, SessionDB(home / "state.db"))
    run_turn(agent, "please track the orchid delivery, 회의록도 정리해줘", "Tracking the orchid delivery now.")
    drain(agent)
    _insert(memory_store_sql, uid="ext-1", source="messages", agent_id=None, scope="agents", role="user",
            content=EXTERNAL_TEXT, author="kakao:friend-1", conversation="messages:kakao:room-1")
    _insert(memory_store_sql, uid="agent:other:m-1", source="agent", agent_id="other", scope="agent", role="user",
            content=OTHER_AGENT_TEXT)
    message, conclusion = _honcho(lina_env)
    node_id = _seed_summary(agent)
    return SimpleNamespace(agent=agent, message=message, conclusion=conclusion, node_id=node_id)


def _call(agent, tool: str, **args) -> dict:
    return json.loads(agent._memory_manager.handle_tool_call(tool, args, session_id=SESSION))


def test_one_search_returns_hits_from_every_source_and_each_ref_opens_its_original(seeded):
    found = _call(seeded.agent, "memory_search", query="orchid", limit=20)

    assert found["skipped"] == []
    by_source = {}
    for hit in found["results"]:
        assert set(hit) >= {"source", "ref", "excerpt", "time", "author", "speaker"}
        by_source.setdefault(hit["source"], []).append(hit)
    assert set(by_source) == {"agent", "messages", "honcho", "lcm"}

    opened = {hit["ref"]: _call(seeded.agent, "memory_open", ref=hit["ref"]) for hit in found["results"]}
    texts = {ref: page["text"] for ref, page in opened.items()}
    agent_texts = {texts[h["ref"]] for h in by_source["agent"]}
    assert agent_texts == {"please track the orchid delivery, 회의록도 정리해줘", "Tracking the orchid delivery now."}
    # The speaker kind memory.messages stores for each row (lina#324): the person's turn is the owner's.
    assert {h["speaker"] for h in by_source["agent"]} == {"owner", "agent"}
    external = by_source["messages"]
    assert [texts[h["ref"]] for h in external] == [EXTERNAL_TEXT]
    assert external[0]["author"] == "kakao:friend-1" and external[0]["time"] == "2026-11-01T09:00:00+00:00"
    assert {texts[h["ref"]] for h in by_source["honcho"]} == {seeded.message.content, seeded.conclusion.content}
    summary = by_source["lcm"]
    assert [h["ref"] for h in summary] == [f"ctx:{seeded.node_id}:{SESSION}"]
    assert opened[summary[0]["ref"]]["text"] == "Summary: the orchid delivery was arranged for tomorrow."
    assert opened[summary[0]["ref"]]["source_ids"] == [1, 2]


def test_an_agent_reads_its_own_rows_and_the_shared_external_rows_never_another_agents(seeded):
    found = _call(seeded.agent, "memory_search", query="orchid", sources=["agent", "messages"])

    refs = [hit["ref"] for hit in found["results"]]
    assert "msg:ext-1" in refs
    assert all(not ref.startswith("msg:agent:other:") for ref in refs)
    assert all(hit["source"] in {"agent", "messages"} for hit in found["results"])
    refused = _call(seeded.agent, "memory_open", ref="msg:agent:other:m-1")
    assert "not readable by this agent" in refused["error"]


def test_a_failing_source_is_skipped_and_the_search_still_answers(seeded, lina_env):
    lina_env.search.side_effect = RuntimeError("honcho is down")

    found = _call(seeded.agent, "memory_search", query="orchid")

    assert [s["source"] for s in found["skipped"]] == ["honcho"]
    assert "honcho is down" in found["skipped"][0]["reason"]
    assert {hit["source"] for hit in found["results"]} == {"agent", "messages", "lcm"}


def test_a_two_character_korean_term_hits_inside_longer_words(seeded):
    found = _call(seeded.agent, "memory_search", query="회의", sources=["agent", "messages"])

    texts = {_call(seeded.agent, "memory_open", ref=hit["ref"])["text"]: hit["source"] for hit in found["results"]}
    assert texts == {"please track the orchid delivery, 회의록도 정리해줘": "agent", EXTERNAL_TEXT: "messages"}


def test_forgotten_unknown_and_malformed_refs(seeded, memory_store_sql):
    memory_store_sql("UPDATE memory.messages SET forgotten_at = now() WHERE uid = 'ext-1'")

    found = _call(seeded.agent, "memory_search", query="orchid", sources=["messages"])
    opened = _call(seeded.agent, "memory_open", ref="msg:ext-1")

    assert found["results"] == []
    assert opened["forgotten"] is True and opened["text"] is None
    assert "not readable" in _call(seeded.agent, "memory_open", ref="msg:agent:default:missing")["error"]
    for ref in ("", "orchid", "msg:", "ltm:m:hm-1", "ctx:abc:search-1"):
        assert "malformed ref" in _call(seeded.agent, "memory_open", ref=ref)["error"], ref
    assert "unknown source" in _call(seeded.agent, "memory_search", query="orchid", sources=["mail"])["error"]


def test_the_vector_leg_finds_an_embedded_row_without_the_query_words(seeded, memory_store_sql):
    from agent.embedding_client import EmbeddingClient, EmbeddingSettings

    uid, message_id = memory_store_sql(
        "SELECT uid, message_id FROM memory.messages WHERE agent_id = 'default' AND role = 'assistant'")[0]
    vector = "[" + ",".join(["1"] + ["0"] * 1023) + "]"
    chunk = memory_store_sql("INSERT INTO vec.chunks (source, agent_id, message_ids, text) VALUES"
                             " ('agent', 'default', %s, 'x') RETURNING chunk_id", ([message_id],))[0][0]
    memory_store_sql("INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) VALUES"
                     " ('chunk', %s, 'default', 'test-embed', 1024, %s::vector)", (chunk, vector))
    answer = {"data": [{"index": 0, "embedding": [1.0] + [0.0] * 1023}]}
    provider = next(p for p in seeded.agent._memory_manager.providers if p.name == "lina-memory")
    provider._embedding = EmbeddingClient(EmbeddingSettings(base_url="http://embed.test/v1", model="test-embed"),
                                          transport=httpx.MockTransport(lambda _r: httpx.Response(200, json=answer)))

    found = _call(seeded.agent, "memory_search", query="flowers", sources=["agent"])

    assert [hit["ref"] for hit in found["results"]] == [f"msg:{uid}"]


def _open_all(agent, ref: str) -> tuple[list[dict], str]:
    """Every page of *ref* by next_offset, and the raw size of the largest tool result."""
    pages, offset, largest = [], 0, 0
    while offset is not None:
        raw = agent._memory_manager.handle_tool_call("memory_open", {"ref": ref, "offset": offset}, session_id=SESSION)
        largest = max(largest, len(raw))
        page = json.loads(raw)
        assert page["offset"] == offset
        pages.append(page)
        offset = page["next_offset"]
    return pages, largest


def test_a_huge_message_opens_in_capped_pages_that_join_to_the_whole_text(seeded, memory_store_sql):
    from plugins.memory.lina.search import OPEN_TEXT_CHARS
    from tools.budget_config import DEFAULT_RESULT_SIZE_CHARS

    line = 'depth-5 summary: "orchid" 회의록 — \\ tab\there\n'
    huge = (line * (256_159 // len(line) + 1))[:256_159]
    _insert(memory_store_sql, uid="agent:default:huge-1", source="agent", agent_id="default", scope="agent",
            role="assistant", content=huge)

    pages, largest = _open_all(seeded.agent, "msg:agent:default:huge-1")

    assert len(pages) == -(-len(huge) // OPEN_TEXT_CHARS)
    assert all(len(p["text"]) <= OPEN_TEXT_CHARS and p["text_chars"] == len(huge) for p in pages)
    assert [p["truncated"] for p in pages] == [True] * (len(pages) - 1) + [False]
    assert "".join(p["text"] for p in pages) == huge
    assert largest < DEFAULT_RESULT_SIZE_CHARS
    assert "past the end" in _call(seeded.agent, "memory_open", ref="msg:agent:default:huge-1",
                                   offset=len(huge) + 1)["error"]
    assert "0 or more" in _call(seeded.agent, "memory_open", ref="msg:agent:default:huge-1", offset=-1)["error"]


def test_a_long_summary_pages_and_a_short_one_opens_whole(seeded):
    from importlib import import_module

    from plugins.memory.lina.search import OPEN_TEXT_CHARS
    SummaryNode = import_module("plugins.context_engine.lcm-x.dag").SummaryNode
    long_summary = "".join(f"Day {i}: the orchid delivery moved again.\n" for i in range(1_500))
    node_id = seeded.agent.context_compressor._dag.add_node(SummaryNode(
        session_id=SESSION, depth=5, summary=long_summary, token_count=15_000, source_token_count=90_000,
        source_ids=[seeded.node_id], source_type="nodes", expand_hint="orchid", earliest_at=1793520000.0,
        latest_at=1793520060.0))

    pages, _ = _open_all(seeded.agent, f"ctx:{node_id}:{SESSION}")
    short = _call(seeded.agent, "memory_open", ref=f"ctx:{seeded.node_id}:{SESSION}")

    assert len(long_summary) > 2 * OPEN_TEXT_CHARS and len(pages) == 3
    assert pages[0]["summary_truncated"] is False
    assert "".join(p["text"] for p in pages) == long_summary
    assert short["text"] == "Summary: the orchid delivery was arranged for tomorrow."
    assert short["truncated"] is False and short["next_offset"] is None and short["text_chars"] == len(short["text"])


def test_the_memory_tools_sit_beside_the_existing_ones(seeded):
    names = seeded.agent._memory_manager.get_all_tool_names()

    assert {"memory_search", "memory_open", "memory_recall", "honcho_search", "honcho_conclude"} <= names


# lina#726: a 200k-character digest holding every query term must not outrank the short, newer records of the same
# event, whether it is a summary the host injected as a user turn or a long paste a person sent.
MOVE_QUERY = "hub move lina-hub cutover"
MOVE_RECORDS = ("hub move done, lina-hub answers on the new address",
                "cutover finished, the old hub is off",
                "lina-hub cutover checked from the phone")


def _ago(days: float) -> str:
    return (datetime.now(UTC) - timedelta(days=days)).isoformat()


def _digest(opener: str) -> str:
    body = "Plan for the hub move to lina-hub and the cutover; backups, keys, services, the phone and the NAS. "
    return (opener + body * (200_000 // len(body) + 1))[:200_000]


@pytest.mark.parametrize("opener", ["[Depth-3 Summary (d3, node 398)]\n## Summary\n", "pasted notes:\n"],
                         ids=["injected-summary", "long-paste"])
def test_short_newer_records_rank_above_a_huge_digest_with_every_term(seeded, memory_store_sql, opener):
    _insert(memory_store_sql, uid="agent:default:digest", source="agent", agent_id="default", scope="agent",
            role="user", content=_digest(opener), ts=_ago(10))
    for day, text in enumerate(MOVE_RECORDS, 1):
        _insert(memory_store_sql, uid=f"agent:default:move-{day}", source="agent", agent_id="default",
                scope="agent", role="user" if day % 2 else "assistant", content=text, ts=_ago(4 - day))

    found = _call(seeded.agent, "memory_search", query=MOVE_QUERY, sources=["agent"])

    refs = [hit["ref"] for hit in found["results"]]
    assert set(refs[:3]) == {f"msg:agent:default:move-{day}" for day in (1, 2, 3)}
    assert ("msg:agent:default:digest" in refs) is (not opener.startswith("[Depth-"))


def test_the_vector_leg_leaves_out_an_injected_summary_turn_too(seeded, memory_store_sql):
    from agent.embedding_client import EmbeddingClient, EmbeddingSettings

    _insert(memory_store_sql, uid="agent:default:summary", source="agent", agent_id="default", scope="agent",
            role="user", content=_digest("[Depth-3 Summary (d3, node 398)]\n## Summary\n"))
    uid, message_id = memory_store_sql("SELECT uid, message_id FROM memory.messages WHERE uid = %s",
                                       ("agent:default:summary",))[0]
    vector = "[" + ",".join(["1"] + ["0"] * 1023) + "]"
    chunk = memory_store_sql("INSERT INTO vec.chunks (source, agent_id, message_ids, text) VALUES"
                             " ('agent', 'default', %s, 'x') RETURNING chunk_id", ([message_id],))[0][0]
    memory_store_sql("INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) VALUES"
                     " ('chunk', %s, 'default', 'test-embed', 1024, %s::vector)", (chunk, vector))
    answer = {"data": [{"index": 0, "embedding": [1.0] + [0.0] * 1023}]}
    provider = next(p for p in seeded.agent._memory_manager.providers if p.name == "lina-memory")
    provider._embedding = EmbeddingClient(EmbeddingSettings(base_url="http://embed.test/v1", model="test-embed"),
                                          transport=httpx.MockTransport(lambda _r: httpx.Response(200, json=answer)))

    found = _call(seeded.agent, "memory_search", query="flowers", sources=["agent"])

    assert f"msg:{uid}" not in [hit["ref"] for hit in found["results"]]


def test_a_persons_bracketed_message_and_a_plain_turn_are_still_found(seeded, memory_store_sql):
    bracketed = {"agent:default:b-1": "[urgent] hub move tonight, keep lina-hub up",
                 "agent:default:b-2": "[Depth chart] hub move order for the team",
                 "agent:default:b-3": "[OUT-OF-BAND USER MESSAGE]\nstop the hub move\n[/OUT-OF-BAND USER MESSAGE]"}
    for uid, text in bracketed.items():
        _insert(memory_store_sql, uid=uid, source="agent", agent_id="default", scope="agent", role="user",
                content=text, ts=_ago(1))

    moved = {hit["ref"] for hit in _call(seeded.agent, "memory_search", query="hub move", sources=["agent"])["results"]}
    orchid = _call(seeded.agent, "memory_search", query="orchid", sources=["agent"])["results"]

    assert moved == {f"msg:{uid}" for uid in bracketed}
    assert {_call(seeded.agent, "memory_open", ref=hit["ref"])["text"] for hit in orchid} == {
        "please track the orchid delivery, 회의록도 정리해줘", "Tracking the orchid delivery now."}


def test_fusion_weighs_a_hits_age_so_a_newer_hit_beats_a_much_older_one_ranked_just_above_it():
    from plugins.memory.lina.search import MemorySearch, Readers

    now = datetime.now(UTC)
    messages = [{"id": "old", "session_id": "hs-1", "content": "hub move plan", "created_at": now - timedelta(days=60)},
                {"id": "new", "session_id": "hs-1", "content": "hub move done", "created_at": now - timedelta(days=1)}]
    manager = SimpleNamespace(search_messages=lambda *_a, **_k: messages, search_conclusions=lambda *_a, **_k: [])
    search = MemorySearch(Readers(store=MagicMock(), honcho=lambda: (manager, "key"), lcm=MagicMock(),
                                  embedder=MagicMock()))

    found = search.search("hub move", sources=["honcho"])

    assert [hit["ref"] for hit in found["results"]] == ["ltm:m:new:hs-1", "ltm:m:old:hs-1"]
