"""Regression for lina#360 at the profile store and Honcho SDK boundaries."""

import json
from contextlib import contextmanager
from types import SimpleNamespace
from unittest.mock import Mock

import pytest

from hermes_cli.heart import rows
from plugins.memory.honcho import client, session
from plugins.memory.lina import LinaMemoryProvider

FORGOTTEN_TASTE = "woodcut prints"
AI_REPRESENTATION = f"The agent prefers {FORGOTTEN_TASTE}."
AI_CARD = f"The agent recommends {FORGOTTEN_TASTE}."
PERSON_CARD = "The owner curates an exhibit."
OWNER_PEER = "owner-peer"
AGENT_PEER = "agent-peer"


@pytest.fixture
def honcho_sdk(monkeypatch):
    sdk = Mock()
    peers = {OWNER_PEER: Mock(), AGENT_PEER: Mock()}
    sdk.peer.side_effect = peers.__getitem__
    agent = peers[AGENT_PEER]
    contexts = {
        OWNER_PEER: SimpleNamespace(representation="The owner plans exhibitions.", peer_card=[PERSON_CARD]),
        AGENT_PEER: SimpleNamespace(representation=AI_REPRESENTATION, peer_card=[AI_CARD]),
    }
    agent.context.side_effect = lambda *, target: contexts[target]
    sdk.session.return_value.context.return_value = SimpleNamespace(messages=[], summary=None)
    sdk.session.return_value.get_peer_configuration.return_value = SimpleNamespace(
        observe_me=None, observe_others=None,
    )
    agent.set_card.side_effect = lambda card, *, target: card
    cfg = client.HonchoClientConfig(
        enabled=True, api_key="synthetic", ai_peer=AGENT_PEER, peer_name=OWNER_PEER,
        recall_sync=True, session_strategy="per-session",
    )
    monkeypatch.setattr(client.HonchoClientConfig, "from_global_config", lambda: cfg)
    monkeypatch.setattr(client, "get_honcho_client", lambda config: sdk)
    monkeypatch.setattr(session, "get_honcho_client", lambda config: sdk)
    return peers


@contextmanager
def _provider(home, session_id):
    provider = LinaMemoryProvider()
    try:
        provider.initialize(session_id, hermes_home=str(home), cwd=str(home))
        yield provider
    finally:
        provider.shutdown()


def test_last_taste_tombstone_keeps_honcho_self_context_out_of_new_lina_sessions(
    tmp_path, monkeypatch, memory_store_sql, honcho_sdk,
):
    home = tmp_path / "profiles" / "agent"
    other = tmp_path / "profiles" / "other"
    home.mkdir(parents=True)
    other.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(other))

    with _provider(home, "before-taste") as provider:
        before = provider.session_card_block()
    assert all(text in before for text in (PERSON_CARD, AI_REPRESENTATION, AI_CARD))

    [(taste_id,)] = memory_store_sql(
        "INSERT INTO memory.tastes(agent_id,target_kind,label,status,origin,valence,strength) "
        "VALUES ('agent','method',%s,'active','temperament','like',0.3) RETURNING id",
        (FORGOTTEN_TASTE,),
    )
    with _provider(home, "with-taste") as provider:
        live = provider.session_card_block()
    assert PERSON_CARD in live and FORGOTTEN_TASTE not in live

    with rows.open_store(home) as store, store.write(), store._writer_cursor() as cur:
        cur.execute(
            "UPDATE memory.tastes SET status='forgotten',forgotten_by='owner' "
            "WHERE id=%s RETURNING live,label",
            (taste_id,),
        )
        assert cur.fetchone() == (False, None)

    with _provider(other, "other-profile") as provider:
        unrelated = provider.session_card_block()
    assert all(text in unrelated for text in (PERSON_CARD, AI_REPRESENTATION, AI_CARD))

    with _provider(home, "after-forget") as provider:
        after = provider.session_card_block()
    assert PERSON_CARD in after
    assert FORGOTTEN_TASTE not in after
    for peer in honcho_sdk.values():
        peer.conclusions_of.assert_not_called()
        peer.set_card.assert_not_called()


def test_lina_authored_honcho_writes_exclude_local_agent_taste(
    tmp_path, honcho_sdk,
):
    fact = "The owner opens the exhibit on Friday."
    with _provider(tmp_path, "write-session") as provider:
        assert PERSON_CARD in provider.session_card_block()
        provider.on_memory_write("add", "memory", f"like: {FORGOTTEN_TASTE}")
        provider.on_memory_write("add", "user", fact)
        card = json.loads(provider.handle_tool_call("honcho_profile", {"card": [fact]}))
        assert card["card"] == [fact]

    agent = honcho_sdk[AGENT_PEER]
    agent.conclusions_of.assert_called_once_with(OWNER_PEER)
    agent.conclusions_of.return_value.create.assert_called_once_with([
        {"content": fact, "session_id": "write-session"},
    ])
    agent.set_card.assert_called_once_with([fact], target=OWNER_PEER)
    owner = honcho_sdk[OWNER_PEER]
    owner.conclusions_of.assert_not_called()
    owner.set_card.assert_not_called()
