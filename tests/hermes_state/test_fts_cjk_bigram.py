"""CJK substring and mixed-script search behavior on the memory store."""

import pytest

from hermes_state import SessionDB


@pytest.fixture
def db(tmp_path):
    with SessionDB(db_path=tmp_path / "state.db") as d:
        d.create_session(session_id="s1", source="cli", model="m")
        d.append_message("s1", role="user", content="웅기가 shared default 프로필을 요청했다")
        d.append_message("s1", role="assistant", content="일본 MCP 후보 우선순위 정리했습니다")
        d.append_message("s1", role="user", content="graphiti daemon looks healthy")
        d.append_message("s1", role="tool", content="일본 tool output blob", tool_name="terminal")
        yield d


def test_two_char_korean_hits_cjk_index(db):
    rows = db.search_messages("웅기", limit=10)
    assert rows and "웅기" in rows[0]["snippet"]
    rows = db.search_messages("일본", limit=10)
    assert rows


def test_mixed_and_ascii_queries(db):
    assert db.search_messages("graphiti", limit=10)
    assert db.search_messages('"shared default" AND 웅기', limit=10)
    assert db.search_messages("우선순위", limit=10)


def test_lone_single_cjk_char_routes_like(db):
    # 1-char CJK terms keep LIKE substring semantics (bigram index only
    # holds unigrams for isolated chars). "가" appears inside 웅기가.
    rows = db.search_messages("가", limit=10)
    assert rows, "LIKE fallback must still find substring matches"


def test_pure_latin_embedded_in_cjk_recovered_via_cjk_index(db):
    """#54242 residual: a pure-Latin query for a token embedded in CJK text
    (no whitespace) misses on unicode61; with the cjk index available the
    zero-result fallback recovers it as an exact ranked token match."""
    db.append_message("s1", role="user", content="修改youer服务端的계획")
    rows = db.search_messages("youer", limit=10)
    assert rows and "youer" in rows[0]["snippet"]
    # Short tokens (<3 chars, no trigram) are also recoverable via cjk.
    db.append_message("s1", role="user", content="에러코드ab확인")
    rows = db.search_messages("ab", limit=10)
    assert rows
