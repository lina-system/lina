"""lina#752: message citations survive conclusion reads without becoming owner evidence."""

import json
from types import SimpleNamespace

import pytest

from plugins.memory.honcho.client import HonchoClientConfig
from plugins.memory.honcho.session import HonchoSession, HonchoSessionManager
from plugins.memory.lina import LinaMemoryProvider, grade
from plugins.memory.lina.search import MemorySearch, Readers, handle_tool_call


def _manager(monkeypatch, *, derived):
    messages = {
        "owner-turn": SimpleNamespace(id="owner-turn", content="The hub routes server requests.",
                                     session_id="conversation", peer_id="owner-peer", created_at=None,
                                     metadata={"lina_speaker": "owner", "lina_grade": "owner"}),
        "agent-turn": SimpleNamespace(id="agent-turn", content="The hub is a herb garden.",
                                     session_id="conversation", peer_id="agent-peer", created_at=None,
                                     metadata={"lina_speaker": "agent", "lina_grade": "agent"}),
    }

    def conclusion(ident, message_ids=(), source_ids=()):
        return SimpleNamespace(id=ident, content="The owner grows herbs." if ident != "owner-fact"
                               else "The hub routes server requests.", created_at=None,
                               observer_id="agent-peer", observed_id="owner-peer", session_id="conversation",
                               source_message_ids=list(message_ids), source_ids=list(source_ids))

    records = {
        "agent-fact": conclusion("agent-fact", ["agent-turn"]),
        "owner-fact": conclusion("owner-fact", ["owner-turn"]),
        "derived-fact": conclusion("derived-fact", source_ids=["agent-fact"]),
        "uncited": conclusion("uncited"),
    }
    target = "derived-fact" if derived else "agent-fact"
    roots = [records[target], records["owner-fact"], records["uncited"]]
    scope = SimpleNamespace(
        query=lambda *args, **kwargs: roots,
        get=records.__getitem__,
        list=lambda **kwargs: SimpleNamespace(items=roots if kwargs.get("page", 1) == 1 else []),
    )
    client = SimpleNamespace(
        peer=lambda ident: SimpleNamespace(conclusions_of=lambda target: scope),
        session=lambda ident: SimpleNamespace(get_message=messages.__getitem__),
        search=lambda *args, **kwargs: list(messages.values()),
    )
    monkeypatch.setattr("plugins.memory.honcho.session.get_honcho_client", lambda config: client)
    manager = HonchoSessionManager(config=HonchoClientConfig())
    manager._cache["session"] = HonchoSession(key="session", user_peer_id="owner-peer",
                                            assistant_peer_id="agent-peer", honcho_session_id="conversation")
    return manager, target


@pytest.mark.parametrize("derived", [False, True])
def test_agent_citations_survive_search_open_and_never_feed_owner_facts(monkeypatch, derived):
    manager, target = _manager(monkeypatch, derived=derived)
    search = MemorySearch(Readers(honcho=lambda: (manager, "session")))

    result = json.loads(handle_tool_call(search, "memory_search", {"query": "hub", "sources": ["honcho"]}))
    found = next(hit for hit in result["results"] if hit["ref"] == f"ltm:c:{target}")

    assert found["source_speakers"] == ["agent"]
    assert found["grade"] == "agent"
    assert found["origin"] == "derived" and found["evidence"] is False
    assert "derived from agent turn; not owner evidence" in grade.recall_text([found])
    opened = json.loads(handle_tool_call(search, "memory_open", {"ref": found["ref"]}))
    assert opened["source_speakers"] == ["agent"] and opened["evidence"] is False
    assert [item["id"] for item in manager.person_conclusions("session")] == ["owner-fact"]
    owner = next(hit for hit in result["results"] if hit["ref"] == "ltm:m:owner-turn:conversation")
    assert owner["speaker"] == "owner" and owner["grade"] == "owner"
    uncited = next(hit for hit in result["results"] if hit["ref"] == "ltm:c:uncited")
    assert uncited["grade"] == "external" and uncited["evidence"] is False


def test_generated_honcho_recall_cannot_precede_or_pose_as_original_evidence(monkeypatch):
    manager, _ = _manager(monkeypatch, derived=True)
    provider = LinaMemoryProvider()
    search = MemorySearch(Readers(honcho=lambda: (manager, "session")))
    monkeypatch.setattr(provider, "_memory_search", lambda: search)

    text = provider._with_external_messages("The owner grows herbs.", "hub")

    assert text.index("The hub routes server requests.") < text.index("The owner grows herbs.")
    assert "derived from agent turn; not owner evidence" in text
    block = text.split("<external-data>", 1)[1].split("</external-data>", 1)[0]
    assert "> The owner grows herbs." in block
    assert "derived; check original record" in block
