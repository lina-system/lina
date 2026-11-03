"""lina#20: the policy table replaces Honcho's text filter (``_INTERNAL_GATEWAY_TURN_RE``).

Every notice the old filter matched by its text is kept out of Honcho by the route it arrives on (the producer's
display kind or the compressor's own row flag), through lina-memory and through the Honcho provider on its own.
A person who types or quotes the same text is a person: their message reaches Honcho as their words.
"""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from plugins.memory.honcho import HonchoMemoryProvider
from plugins.memory.lina.policy import load_policy

REPLACED = load_policy().replaces


class _Manager:
    def __init__(self):
        self.written = []

    def get_or_create(self, session_key, **_kwargs):
        manager = self

        class _Session:
            def add_message(self, role, content, **kwargs):
                manager.written.append((role, content, kwargs.get("author_peer_id")))

        return _Session()

    def save(self, _session):
        pass

    def resolve_author_peer_id(self, *_args, **_kwargs):
        return None

    def _resolve_user_peer_id(self, _key):
        return "alice"


def _honcho() -> HonchoMemoryProvider:
    provider = HonchoMemoryProvider()
    provider._config = SimpleNamespace(enabled=True, api_key="k", base_url=None, message_max_chars=25000,
                                       save_messages=True, a2a_sessions=True, workspace_id="ws", ai_peer="lina-bot")
    provider._manager = _Manager()
    provider._session_key = "telegram-dm-1001"
    provider._session_initialized = True
    return provider


def _arrival(row: dict, text: str) -> list[dict]:
    """The transcript the notice's producer leaves: its signal on the turn's user row."""
    user = {"role": "user", "content": text}
    if row.get("display_kind"):
        user["display_kind"] = row["display_kind"]
    if row.get("compressor_row"):
        user["_compressed_summary"] = True
    return [{"role": "user", "content": "earlier question"}, {"role": "assistant", "content": "earlier answer"},
            user, {"role": "assistant", "content": "reply"}]


def _written(provider: HonchoMemoryProvider) -> list:
    if provider._sync_thread is not None:
        provider._sync_thread.join(timeout=10)
    return provider._manager.written


@pytest.mark.parametrize("row", REPLACED, ids=[r["text"][:40] for r in REPLACED])
def test_each_old_filter_alternative_stays_out_of_honcho_by_its_route(row):
    provider = _honcho()

    provider.sync_turn(row["text"], "reply", messages=_arrival(row, row["text"]))

    assert load_policy().rule(row["route"]).honcho is False
    assert _written(provider) == []


@pytest.mark.parametrize("row", REPLACED, ids=[r["text"][:40] for r in REPLACED])
def test_each_old_filter_alternative_stays_out_of_honcho_through_lina_memory(row, monkeypatch):
    from plugins.memory.lina import LinaMemoryProvider

    lina = LinaMemoryProvider()
    lina._honcho, lina._honcho_on = _honcho(), True
    monkeypatch.setattr("plugins.memory.lina.lcm.hand_turn", lambda *a, **k: True)

    lina.sync_turn(row["text"], "reply", local_messages=_arrival(row, row["text"]), messages=_arrival(row, row["text"]))
    lina._drain()

    assert _written(lina._honcho) == []


@pytest.mark.parametrize("row", REPLACED, ids=[r["text"][:40] for r in REPLACED])
@pytest.mark.parametrize("shape", ["{text} — what does that mean?", "I saw '{text}' in the log today"],
                         ids=["starts-with-it", "quotes-it"])
def test_a_person_typing_an_old_filter_string_reaches_honcho_as_their_words(row, shape):
    provider = _honcho()
    text = shape.format(text=row["text"])

    provider.sync_turn(text, "reply", messages=_arrival({}, text))

    assert _written(provider) == [("user", text, None), ("assistant", "reply", None)]
