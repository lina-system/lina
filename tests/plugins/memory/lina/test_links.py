"""lina#324 (absorbing #313): a person's handles on several channels join only through links the owner confirmed.
Searches run through the real agent's memory tools against the test Postgres."""

from __future__ import annotations

import json

import pytest

from tests.plugins.memory.lina.harness import make_agent, write_home

MINA_KAKAO = "messenger_author:kakao:k-1"
OTHER_MINA = "messenger_author:kakao:k-2"
MINA_LINE = "messenger_author:line:u-5"


@pytest.fixture
def agent(tmp_path, monkeypatch, lina_env, memory_store_sql):
    from hermes_state import SessionDB
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    # Two people named Mina on one messenger, and the first Mina on another one.
    for uid, author, text in (("x1", "kakao:k-1", "picnic at the lake on Sunday"),
                              ("x2", "kakao:k-2", "picnic gear is in my car"),
                              ("x3", "line:u-5", "picnic snacks are my job")):
        memory_store_sql("INSERT INTO memory.messages (uid, source, agent_id, scope, role, content, author, native,"
                         " content_sha256) VALUES (%s, 'messages', NULL, 'agents', 'user', %s, %s, %s::jsonb, 'x')",
                         (f"messages:{uid}", text, author, json.dumps({"author_name": "Mina"})))
    db = SessionDB(home / "state.db")
    agent = make_agent("links-1", db)
    yield agent
    agent.shutdown_memory_provider()
    db.close()


def _tool(agent, name: str, **args) -> dict:
    provider = next(p for p in agent._memory_manager.providers if p.name == "lina-memory")
    return json.loads(provider.handle_tool_call(name, args))


def _picnic(agent, person: str) -> set[str]:
    return {hit["ref"] for hit in _tool(agent, "memory_search", query="picnic", person=person)["results"]}


def test_a_shared_name_never_links_and_a_candidate_joins_nothing(agent):
    registry = next(p for p in agent._memory_manager.providers if p.name == "lina-memory")._registry
    mina = registry.person_of(("messenger_author", "kakao:k-1"))
    registry.link(("messenger_author", "line:u-5"), mina, status="candidate", evidence="same display name")

    assert _picnic(agent, MINA_KAKAO) == {"msg:messages:x1"}
    assert _picnic(agent, OTHER_MINA) == {"msg:messages:x2"}
    assert _tool(agent, "memory_link", action="list", person=MINA_KAKAO)["links"] == [
        {"handle": MINA_LINE, "status": "candidate", "evidence": "same display name"}]


def test_one_owner_sentence_confirms_and_one_undoes_a_link(agent):
    confirmed = _tool(agent, "memory_link", action="confirm", handle=MINA_LINE, person=MINA_KAKAO,
                      said="Mina on LINE is the same Mina as on KakaoTalk")
    both = _picnic(agent, MINA_KAKAO)
    undone = _tool(agent, "memory_link", action="undo", handle=MINA_LINE, person=MINA_KAKAO,
                   said="No, the LINE one is someone else")

    assert confirmed["status"] == "confirmed"
    assert both == {"msg:messages:x1", "msg:messages:x3"}
    assert undone["status"] == "undone"
    assert _picnic(agent, MINA_KAKAO) == {"msg:messages:x1"}
    assert "error" in _tool(agent, "memory_link", action="confirm", handle=MINA_LINE, person=MINA_KAKAO)


def test_an_alias_list_imports_as_confirmed_links(agent, tmp_path):
    aliases = tmp_path / "aliases.txt"
    aliases.write_text(f"# Mina, by hand\n{MINA_KAKAO}  {MINA_LINE}\n\n", encoding="utf-8")
    broken = tmp_path / "broken.txt"
    broken.write_text(f"{MINA_KAKAO} kakao-name:Mina\n", encoding="utf-8")

    assert "error" in _tool(agent, "memory_link", action="import", path=str(broken))
    assert _picnic(agent, MINA_KAKAO) == {"msg:messages:x1"}
    assert _tool(agent, "memory_link", action="import", path=str(aliases)) == {"people": 1, "linked": 1}
    assert _picnic(agent, MINA_LINE) == {"msg:messages:x1", "msg:messages:x3"}
    assert _picnic(agent, OTHER_MINA) == {"msg:messages:x2"}
