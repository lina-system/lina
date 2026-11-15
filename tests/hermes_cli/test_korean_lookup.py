"""The owner's own word for a concept (lina#379): read from his rows only, matched through Kiwi, never coined.

All text is synthetic."""

import json

import pytest

from hermes_cli.korean import lookup
from hermes_state_store import MessageStore

pytest.importorskip("kiwipiepy", reason="Korean analysis needs the 'korean' extra (kiwipiepy), not installed here")

AGENT = "alpha"


@pytest.fixture
def store():
    found = MessageStore(AGENT)
    yield found
    found.close()


@pytest.fixture
def say(memory_store_sql):
    count = iter(range(1, 1000))

    def run(text: str, speaker: str = "owner", *, agent: str = AGENT, forgotten: bool = False) -> str:
        uid = f"agent:{agent}:fixture-{next(count)}"
        role = "user" if speaker == "owner" else "assistant"
        memory_store_sql("INSERT INTO memory.messages (uid, source, agent_id, role, content, content_sha256, speaker,"
                         " forgotten_at) VALUES (%s, 'agent', %s, %s, %s, 'fixture', %s,"
                         " CASE WHEN %s THEN now() END)", (uid, agent, role, text, speaker, forgotten))
        return uid
    return run


def test_endings_and_particles_resolve_to_one_stem(store, say):
    assert lookup.stems("봤어") == lookup.stems("봤다") == lookup.stems("보고") == ["보다"]
    uids = [say("어제 그 영화 봤어"), say("나도 봤다"), say("보고 나서 얘기하자")]

    for concept in ("보고", "보다"):
        assert lookup.owner_terms(store, concept) == [lookup.OwnerTerm("보다", 3, tuple(reversed(uids)))]


def test_only_the_owners_own_rows_are_read_and_no_text_comes_back(store, say):
    owner = say("취향 목록 정리해줘")
    say("취향 목록을 정리했어요", speaker="agent")
    say("취향목록 공유합니다", speaker="outside")
    say("취향 목록은 지워줘", forgotten=True)
    say("취향 목록 보여줘", agent="beta")

    found = lookup.lookup(store, "취향 목록")

    assert found["terms"] == [{"term": "취향 목록", "count": 1, "uids": [owner]}]
    assert "정리해줘" not in json.dumps(found, ensure_ascii=False)


def test_an_english_term_gives_the_korean_words_the_owner_used_beside_it(store, say):
    # The design doc says "taste slot"; only the owner's message ties it to his word.
    say("A taste slot holds one preference per row.", speaker="agent")
    owner = say("taste slot은 취향 목록이라고 부르자")
    say("취향 목록에 커피 넣어줘")

    by_english = lookup.owner_terms(store, "Taste Slot")
    by_korean = lookup.owner_terms(store, "취향 목록")

    assert by_english == [lookup.OwnerTerm("취향 목록", 1, (owner,))]
    assert lookup.preferred_term(by_english) == lookup.preferred_term(by_korean) == "취향 목록"
    assert by_korean[0].count == 2


def test_a_concept_the_owner_never_named_is_described_not_coined(store, say):
    say("taste slot은 취향 목록이라고 부르자")

    assert lookup.lookup(store, "mood shelf") == {"concept": "mood shelf", "terms": [], "use": None,
                                                  "hint": lookup.HINT}
    assert lookup.lookup(store, "기분 선반")["hint"] == lookup.HINT
    owner_words = [term.term for term in lookup.owner_terms(store, "취향 목록")]
    reply = "기분선반에 넣을게요. 취향목록과 일정표도 볼게요."
    coined = [c for c in lookup.compounds(reply) if lookup.is_coined(c, owner_words, lookup.reference_words())]
    assert coined == ["기분선반"]
