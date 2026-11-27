"""lina#446: ``brain_search``, ``brain_get`` and ``brain_links`` through the real agent's memory manager, on the test
Postgres: brain pages found by the Korean keyword leg and the semantic leg, every hit with its source, grade and
``quote_as_external``, a page opened with a bounded timeline, a walk of up to two links, and ``memory_search`` /
``memory_open`` forwarding to the new tools. The wiki is synthetic; the embedder answers from a fixed concept table."""

from __future__ import annotations

import json
from pathlib import Path

import httpx
import pytest

from tests.plugins.memory.lina.harness import make_agent, write_home

SESSION = "brain-1"
PAGES = {
    "people/미나.md": "---\ntitle: 미나\naliases: [Mina]\ngrade: owner\n---\n\n엔지니어. [[믹싱]]을 맡는다.\n\n---\n\n"
                     "## Timeline\n\n- 2026-03-01 | owner | messages:room-1 | 처음 만남\n"
                     "- 2026-03-05 | external | mail:7 | 계약서를 보냈다\n"
                     "- 2026-03-09 | agent | run:r-1 | 믹싱 일정 정리\n",
    "concepts/믹싱.md": "---\ntitle: 믹싱\ngrade: agent\n---\n\n여러 트랙을 한 곡으로 만드는 작업. 다음은 [[마스터링]].\n",
    "concepts/마스터링.md": "---\ntitle: 마스터링\n---\n\n믹싱이 끝난 곡의 음압을 다듬는다.\n",
    "concepts/리버브.md": "---\ntitle: 리버브\ngrade: agent\n---\n\n공간의 잔향을 더하는 효과.\n",
    "concepts/기타 줄 교체.md": "---\ntitle: 기타 줄 교체\ngrade: agent\n---\n\n공연 전주에 간다.\n",
    "sources/기사.md": "---\ntitle: 음악 기사\n---\n\n어느 잡지의 믹싱 기사. 출처: 웹.\n",
}
# The fake embedder's whole vocabulary: a text's vector has a 1 at the dimension of each concept it names.
CONCEPTS = {"잔향": 0, "울림": 0, "트랙": 1, "음압": 2}


def _vector(text: str) -> list[float]:
    vector = [0.0] * 1024
    for word, dimension in CONCEPTS.items():
        if word in text:
            vector[dimension] = 1.0
    vector[1023] = 0.01
    return vector


def _embedder():
    from agent.embedding_client import EmbeddingClient, EmbeddingSettings

    def answer(request: httpx.Request) -> httpx.Response:
        texts = json.loads(request.content)["input"]
        return httpx.Response(200, json={"data": [{"index": i, "embedding": _vector(t)} for i, t in enumerate(texts)]})
    return EmbeddingClient(EmbeddingSettings(base_url="http://embed.test/v1", model="test-embed"),
                           transport=httpx.MockTransport(answer))


def _write(root: Path, pages: dict[str, str]) -> Path:
    for name, body in pages.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(body, encoding="utf-8")
    return root


def _import_and_index(root: Path, embedder):
    from hermes_cli.brain.resolver import DEFAULT_MAPPING, mapping_from
    from hermes_cli.brain.scan import scan
    from hermes_cli.brain.search import build_index
    from hermes_cli.brain.store import owner_connection, sync
    with owner_connection() as conn:
        sync(conn, scan(root, mapping_from(DEFAULT_MAPPING)))
        return build_index(conn, embedder, analyzers=("bigram",))


@pytest.fixture
def agent(tmp_path, monkeypatch, lina_env):
    from hermes_state import SessionDB
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return make_agent(SESSION, SessionDB(home / "state.db"))


@pytest.fixture
def wiki(tmp_path):
    return _write(tmp_path / "wiki", PAGES)


def _provider(agent):
    return next(p for p in agent._memory_manager.providers if p.name == "lina-memory")


def _call(agent, tool: str, **args) -> dict:
    return json.loads(agent._memory_manager.handle_tool_call(tool, args, session_id=SESSION))


@pytest.mark.parametrize("route", ["sequential", "parallel"])
def test_search_receives_the_session_that_dispatched_the_call(agent, monkeypatch, route):
    from agent.inline_tool_executors import InlineToolContext, resolve_invoke_tool_executor
    from agent.tool_executor import _resolve_sequential_dispatch, _ToolCallRef
    from plugins.memory.lina import search

    received = []
    store_lists = search.MemorySearch._store_lists

    def record(engine, *args, **kwargs):
        received.append(kwargs.get("session_id"))
        return store_lists(engine, *args, **kwargs)

    monkeypatch.setattr(search.MemorySearch, "_store_lists", record)
    payload = {"kinds": ["agent"], "query": "synthetic", "session_id": "model-input"}
    for session in ("session-a", "session-b"):
        agent.session_id = session
        if route == "sequential":
            ref = _ToolCallRef("brain_search", payload, "task", "call", [])
            dispatch = _resolve_sequential_dispatch(agent, ref, [])
        else:
            invoke = resolve_invoke_tool_executor(agent, "brain_search")
        agent.session_id = "next-session"
        _provider(agent).on_session_switch(agent.session_id)
        result = (dispatch.execute(payload) if route == "sequential"
                  else invoke(agent, payload, InlineToolContext("task")))
        assert json.loads(result)["skipped"] == []
    assert received == ["session-a", "session-b"]


def test_hits_carry_source_and_grade_and_an_external_page_is_flagged_for_quoting(agent, wiki):
    _import_and_index(wiki, None)

    found = _call(agent, "brain_search", query="믹싱", kinds=["brain"])

    by_ref = {hit["ref"]: hit for hit in found["results"]}
    assert {"page:미나", "page:믹싱", "page:기사"} <= set(by_ref)
    assert all(hit["source"] == "brain" for hit in found["results"])
    assert (by_ref["page:미나"]["grade"], by_ref["page:미나"]["quote_as_external"]) == ("owner", False)
    assert (by_ref["page:믹싱"]["grade"], by_ref["page:믹싱"]["quote_as_external"]) == ("agent", False)
    assert (by_ref["page:기사"]["grade"], by_ref["page:기사"]["quote_as_external"]) == ("external", True)
    assert by_ref["page:기사"]["title"] == "음악 기사" and by_ref["page:기사"]["type"] == "source"
    assert [skip["source"] for skip in found["skipped"]] == ["brain"]
    assert "semantic" in found["skipped"][0]["reason"]


def test_the_keyword_leg_reads_through_particles_and_spacing(agent, wiki):
    _import_and_index(wiki, None)

    def top(query: str) -> str:
        return _call(agent, "brain_search", query=query, kinds=["brain"])["results"][0]["ref"]

    assert top("기타줄을 언제 갈지") == "page:기타-줄-교체"
    assert top("Mina랑 한 일") == "page:미나"


def test_the_semantic_leg_finds_a_page_that_shares_no_word_with_the_query(agent, wiki):
    embedder = _embedder()
    report = _import_and_index(wiki, embedder)
    _provider(agent)._embedding = embedder

    found = _call(agent, "brain_search", query="소리 울림 줄이기", kinds=["brain"])

    assert report.vectors == len(PAGES)
    assert found["skipped"] == []
    assert found["results"][0]["ref"] == "page:리버브"


def test_a_page_type_filter_keeps_only_those_pages(agent, wiki):
    _import_and_index(wiki, None)

    found = _call(agent, "brain_search", query="믹싱", types=["person"])

    assert [hit["ref"] for hit in found["results"]] == ["page:미나"]
    assert "unknown page type" in _call(agent, "brain_search", query="믹싱", types=["album"])["error"]


def test_brain_get_opens_a_page_by_any_name_with_a_bounded_graded_timeline(agent, wiki):
    _import_and_index(wiki, None)

    page = _call(agent, "brain_get", ref="Mina", timeline=2)

    assert page["slug"] == "미나" and page["grade"] == "owner" and page["quote_as_external"] is False
    assert "엔지니어" in page["text"]
    assert [(e["date"], e["grade"], e["quote_as_external"]) for e in page["timeline"]] == [
        ("2026-03-09", "agent", False), ("2026-03-05", "external", True)]
    assert page["timeline_total"] == 3 and page["timeline_truncated"] is True
    assert _call(agent, "brain_get", ref="page:미나")["slug"] == "미나"
    assert _call(agent, "brain_get", ref="people/미나.md")["slug"] == "미나"
    assert "no brain page" in _call(agent, "brain_get", ref="없는 페이지")["error"]


def test_brain_links_walks_up_to_two_links_by_relation(agent, wiki):
    _import_and_index(wiki, None)

    near = _call(agent, "brain_links", ref="page:미나")
    far = _call(agent, "brain_links", ref="미나", depth=2, direction="out")

    assert [(n["slug"], n["depth"]) for n in near["nodes"]] == [("믹싱", 1)]
    assert [(n["slug"], n["depth"]) for n in far["nodes"]] == [("믹싱", 1), ("마스터링", 2)]
    assert {(e["from"], e["to"], e["type"]) for e in far["edges"]} == {("미나", "믹싱", "mentions"),
                                                                       ("믹싱", "마스터링", "mentions")}
    assert all(n["ref"] == f"page:{n['slug']}" and "quote_as_external" in n for n in far["nodes"])
    assert _call(agent, "brain_links", ref="미나", relations=["works_with"])["nodes"] == []
    assert "depth is 1 or 2" in _call(agent, "brain_links", ref="미나", depth=3)["error"]


def test_memory_search_and_memory_open_forward_to_the_new_tools_for_memory_only(agent, wiki):
    _import_and_index(wiki, None)

    old = _call(agent, "memory_search", query="믹싱")
    new = _call(agent, "brain_search", query="믹싱")

    assert {"brain_search", "brain_get", "brain_links", "memory_search", "memory_open"} <= (
        agent._memory_manager.get_all_tool_names())
    assert all(hit["source"] != "brain" for hit in old["results"])
    assert any(hit["source"] == "brain" for hit in new["results"])
    assert "use brain_search" in _call(agent, "memory_search", query="믹싱", sources=["brain"])["error"]
    assert _call(agent, "memory_open", ref="page:미나")["slug"] == "미나"
    assert "malformed ref" in _call(agent, "memory_open", ref="미나")["error"]


def test_the_index_rewrites_only_the_pages_whose_file_changed(wiki):
    embedder = _embedder()
    first = _import_and_index(wiki, embedder)
    again = _import_and_index(wiki, embedder)
    (wiki / "concepts/리버브.md").write_text("---\ntitle: 리버브\n---\n\n울림을 더한다.\n", encoding="utf-8")
    changed = _import_and_index(wiki, embedder)

    assert (first.rewritten_terms, first.vectors) == ({"bigram": len(PAGES)}, len(PAGES))
    assert (again.rewritten_terms, again.vectors) == ({"bigram": 0}, 0)
    assert (changed.rewritten_terms, changed.vectors) == ({"bigram": 1}, 1)


def test_the_keyword_leg_still_answers_when_the_embedding_endpoint_fails(agent, wiki):
    from agent.embedding_client import EmbeddingClient, EmbeddingSettings
    _import_and_index(wiki, _embedder())
    _provider(agent)._embedding = EmbeddingClient(
        EmbeddingSettings(base_url="http://embed.test/v1", model="test-embed"),
        transport=httpx.MockTransport(lambda request: httpx.Response(500, json={"error": "down"})))

    found = _call(agent, "brain_search", query="믹싱", kinds=["brain"])

    assert {"page:미나", "page:믹싱"} <= {hit["ref"] for hit in found["results"]}
    assert [skip["source"] for skip in found["skipped"]] == ["brain"]
    assert "semantic leg failed" in found["skipped"][0]["reason"]


def test_an_index_rerun_after_a_merge_reads_the_names_the_page_gained(agent, wiki):
    from hermes_cli.brain.resolver import DEFAULT_MAPPING, mapping_from
    from hermes_cli.brain.scan import scan
    from hermes_cli.brain.search import build_index
    from hermes_cli.brain.store import owner_connection, sync
    embedder = _embedder()
    _import_and_index(wiki, embedder)
    _provider(agent)._embedding = embedder
    assert _call(agent, "brain_search", query="마스터링", kinds=["brain"])["results"][0]["ref"] == "page:마스터링"

    with owner_connection() as conn:
        sync(conn, scan(wiki, mapping_from(DEFAULT_MAPPING), (("마스터링", "믹싱"),)))
        report = build_index(conn, embedder, analyzers=("bigram",))

    assert report.rewritten_terms["bigram"] == 1  # 믹싱 gained a name; its text did not change
    assert report.vectors == 1
    assert _call(agent, "brain_search", query="마스터링", kinds=["brain"])["results"][0]["ref"] == "page:믹싱"



def test_a_path_two_imported_roots_both_have_is_ambiguous_but_an_exact_slug_still_opens(agent, tmp_path):
    first = _write(tmp_path / "first", {"people/x.md": "---\ntitle: First X\n---\n\nthe first root"})
    second = _write(tmp_path / "second", {"people/x.md": "---\ntitle: Second X\n---\n\nthe second root",
                                          "teams/x.md": "---\ntitle: Team X\n---\n\na team"})
    _import_and_index(first, None)
    _import_and_index(second, None)

    ambiguous = _call(agent, "brain_get", ref="people/x.md")
    assert "several pages" in ambiguous["error"]
    assert "x" in ambiguous["error"] and "people/x" in ambiguous["error"]
    assert _call(agent, "brain_get", ref="x")["title"] == "First X"
    assert _call(agent, "brain_get", ref="people/x")["title"] == "Second X"
    assert "several pages" in _call(agent, "brain_links", ref="people/x.md")["error"]
