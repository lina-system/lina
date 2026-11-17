"""Original records remain available when newer assistant answers dominate recall."""

from __future__ import annotations

import json
from datetime import UTC, datetime
from types import SimpleNamespace

import pytest

from plugins.memory.lina import grade
from plugins.memory.lina.search import Hit, MemorySearch, Readers, fuse, handle_tool_call


@pytest.mark.parametrize("source,speaker", [("agent", "owner"), ("lcm", "summary"), ("repo", "file")])
def test_original_records_precede_repeated_recent_answers(source, speaker):
    original = Hit(source, "original", "The hub is a server.", "2020-01-01T00:00:00+00:00",
                   None, speaker, grade="owner")
    answer = Hit("agent", "answer", "The hub is a herb garden.", "2026-11-01T00:00:00+00:00",
                 None, "agent", grade="agent")
    results = fuse([[answer, original], [answer]], 2, datetime(2026, 11, 1, tzinfo=UTC))

    assert [hit["ref"] for hit in results] == ["original", "answer"]
    assert results[0]["origin"] == "original_record"
    assert results[1]["origin"] == "agent_answer"
    assert results[1]["evidence"] is False
    assert "you said before" in grade.recall_text(results).lower()
    assert "not evidence" in grade.recall_text(results).lower()


class _Store:
    def __init__(self):
        self.rows = [
            SimpleNamespace(uid=f"answer-{i}", source="agent", content="The hub is a herb garden.",
                            at="2026-11-01T00:00:00+00:00", speaker="agent", speaker_ref=None,
                            author="assistant", grade="agent")
            for i in range(12)
        ]
        self.rows.append(SimpleNamespace(
            uid="owner", source="agent", content="The hub is a server.",
            at="2020-01-01T00:00:00+00:00", speaker="owner", speaker_ref=None, author="user", grade="owner"))

    def search_memory(self, query, *, sources, limit, authors, speakers):
        return [row for row in self.rows if not speakers or row.speaker in speakers][:limit]

    def search_memory_vectors(self, vector, *, model, sources, limit, speakers):
        return [row for row in self.rows if not speakers or row.speaker in speakers][:limit]

    def search_parts(self, query, *, limit):
        return []

    def has_memory_vectors(self, **kwargs):
        return True

    def open_memory(self, uid):
        row = next(row for row in self.rows if row.uid == uid)
        return SimpleNamespace(**vars(row), role="user" if row.speaker == "owner" else "assistant",
                               source_id=None, conversation_id="synthetic", session_id="synthetic", forgotten=False)


@pytest.mark.parametrize("semantic", [False, True])
def test_original_turn_survives_source_limit_and_open_preserves_origin(semantic):
    store = _Store()
    if semantic:
        store.search_memory = lambda *args, **kwargs: []
    embedder = SimpleNamespace(model="synthetic", embed_one=lambda text: [1.0])
    search = MemorySearch(Readers(store=lambda: store, embedder=lambda: embedder if semantic else None))

    result = json.loads(handle_tool_call(search, "memory_search", {"query": "hub", "sources": ["agent"], "limit": 1}))
    assert [hit["ref"] for hit in result["results"]] == ["msg:owner"]
    opened = json.loads(handle_tool_call(search, "memory_open", {"ref": "msg:answer-0"}))
    assert opened["origin"] == "agent_answer"
    assert opened["evidence"] is False
    assert opened["text"] == "The hub is a herb garden."


@pytest.mark.parametrize("semantic", [False, True])
def test_unattributed_records_stay_searchable_without_becoming_owner_evidence(semantic):
    store = _Store()
    store.rows = [SimpleNamespace(
        uid="unattributed", source="agent", content="The hub is a server.",
        at="2020-01-01T00:00:00+00:00", speaker="unknown", speaker_ref=None, author=None, grade="external")]
    if semantic:
        store.search_memory = lambda *args, **kwargs: []
    embedder = SimpleNamespace(model="synthetic", embed_one=lambda text: [1.0])
    search = MemorySearch(Readers(store=lambda: store, embedder=lambda: embedder if semantic else None))

    found = search.search("hub", sources=["agent"], limit=1)["results"]

    assert [hit["ref"] for hit in found] == ["msg:unattributed"]
    assert found[0]["speaker"] == "unknown"
    assert found[0]["grade"] == "external"
    assert found[0]["evidence"] is False
