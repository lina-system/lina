"""lina#380: a Korean agent's turn carries the owner's words for the concepts in it, and only those.

The real ``AIAgent`` with ``memory.provider: lina-memory`` and the test store; the model client is stubbed and each
request it receives is read back. All text is synthetic.
"""

from __future__ import annotations

import json

import pytest

from plugins.memory.lina.korean import HEADING, LOOKUP_HINT
from tests.plugins.memory.lina.harness import drain, make_agent, run_turn, write_home

pytest.importorskip("kiwipiepy", reason="Korean analysis needs the 'korean' extra (kiwipiepy), not installed here")

ENGLISH_DOC = "The design doc says each taste slot holds one preference and the planner reads every slot. " * 5


def _home(tmp_path, monkeypatch, language: str):
    from agent.i18n import reset_language_cache
    home = write_home(tmp_path / ".hermes")
    with (home / "config.yaml").open("a", encoding="utf-8") as config:
        config.write(f"display:\n  language: {language}\n")
    monkeypatch.setenv("HERMES_HOME", str(home))
    reset_language_cache()
    return home


@pytest.fixture
def korean_home(tmp_path, monkeypatch, lina_env):
    return _home(tmp_path, monkeypatch, "ko")


@pytest.fixture
def english_home(tmp_path, monkeypatch, lina_env):
    return _home(tmp_path, monkeypatch, "en")


@pytest.fixture
def word_list(memory_store_sql):
    from hermes_cli.korean import words
    with words.owner_connection() as conn:
        words.add(conn, "슬롯", "자리", "calque")


def _turns(home, *users: str) -> list[str]:
    """The last message of each turn's request (the turn's user message with its found memory), serialized."""
    from hermes_state import SessionDB
    db = SessionDB(home / "state.db")
    agent = make_agent("korean-turns", db)
    sent, history = [], None
    try:
        for n, user in enumerate(users):
            result = run_turn(agent, user, f"reply {n}", history)
            sent.append(json.dumps(agent.client.chat.completions.create.call_args.kwargs["messages"][-1],
                                   ensure_ascii=False))
            history = result["messages"]
            drain(agent)
    finally:
        agent.shutdown_memory_provider()
        db.close()
    return sent


def test_a_word_of_the_list_brings_its_entry_and_an_unrelated_turn_brings_nothing(korean_home, word_list):
    named, unrelated = _turns(korean_home, "슬롯을 비워줘", "오늘 정원에 물 줬어")

    assert HEADING in named and "'슬롯' 말고 '자리'" in named
    assert HEADING not in unrelated and "<memory-context>" not in unrelated


def test_an_english_term_brings_the_owners_own_word(korean_home, word_list):
    _, named = _turns(korean_home, "taste slot은 취향 목록이라고 부르자", "taste slot 다시 정리해줘")

    assert "- taste slot: 주인의 말은 '취향 목록'" in named
    assert "말고" not in named


def test_an_english_document_brings_the_lookup_hint(korean_home, word_list):
    (document,) = _turns(korean_home, ENGLISH_DOC)

    assert LOOKUP_HINT.replace("\n", " ") in json.loads(document)["content"].replace("\n", " ")


def test_an_agent_whose_language_is_not_korean_gets_no_entries(english_home, word_list):
    (named,) = _turns(english_home, "슬롯을 비워줘")

    assert HEADING not in named and "자리" not in named
