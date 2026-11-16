"""Synthetic behavior contracts for deterministic reply checks (lina#381)."""

from datetime import UTC, datetime

import pytest

from hermes_cli.korean import checks
from hermes_cli.korean.words import WordEntry


def entry(avoid, use):
    return WordEntry(1, avoid, use, "other", None, datetime(2026, 1, 1, tzinfo=UTC))


@pytest.mark.parametrize(("noun", "particle", "expected"), [
    ("단어", "을", "를"), ("단어", "이", "가"), ("단어", "은", "는"),
    ("단어", "과", "와"), ("단어", "으로", "로"),
    ("책", "를", "을"), ("책", "가", "이"), ("책", "는", "은"),
    ("책", "와", "과"), ("책", "로", "으로"),
    ("길", "으로", "로"), ("길", "로", "로"),
])
def test_known_nouns_reselect_particles_without_changing_surroundings(noun, particle, expected):
    text = f"  오늘은 {noun}{particle} 천천히 살펴보고 내용을 기록했습니다.\n"
    result = checks.check_reply(text, [entry("낱말", noun)], names=())
    assert result.text == f"  오늘은 {noun}{expected} 천천히 살펴보고 내용을 기록했습니다.\n"
    assert not result.dropped


def test_swap_reselects_particle_and_never_rewrites_a_substring_or_cascades():
    text = "슬롯을 보았습니다. 슬롯머신은 그대로 두고 나머지 내용을 기록합니다."
    result = checks.check_reply(text, [entry("슬롯", "자리"), entry("자리", "공간")], names=())
    assert result.text == "자리를 보았습니다. 슬롯머신은 그대로 두고 나머지 내용을 기록합니다."
    assert [(e.start, e.end) for e in result.edits] == [(0, 3)]


@pytest.mark.parametrize(("text", "avoid", "use", "reason", "names"), [
    ("오늘 12개를 두고 나머지 내용은 그대로 보존합니다.", "12개", "13개", "number", ()),
    ("오늘 두 개를 두고 나머지 내용은 그대로 보존합니다.", "두 개", "세 개", "number", ()),
    ("오늘 한 개를 두고 나머지 내용은 그대로 보존합니다.", "한", "두", "number", ()),
    ("오늘 열두 개를 두고 나머지 내용은 그대로 보존합니다.", "열두", "열세", "number", ()),
    ("합계는 다섯입니다. 나머지 내용은 그대로 보존합니다.", "다섯입니다", "여섯입니다", "number", ()),
    ("오늘 안 합니다. 나머지 설명은 그대로 보존합니다.", "안", "꼭", "negation", ()),
    ("오늘 하지 않습니다. 나머지 설명은 그대로 보존합니다.", "않습니다", "합니다", "negation", ()),
    ("오늘 못합니다. 나머지 설명은 그대로 보존합니다.", "못합니다", "합니다", "negation", ()),
    ("오늘 가지 마세요. 나머지 설명은 그대로 보존합니다.", "가지 마세요", "가세요", "negation", ()),
    ("오늘 없어요. 나머지 설명은 그대로 보존합니다.", "없어요", "있어요", "negation", ()),
    ("가온이 왔습니다. 나머지 설명은 그대로 보존합니다.", "가온", "다온", "name", ("가온", "다온")),
    ("오늘 자리가 비었습니다. 나머지 설명은 그대로 보존합니다.", "자리", "가온", "name", ("가온",)),
])
def test_protected_changes_drop_the_entire_fix(text, avoid, use, reason, names):
    result = checks.check_reply(text, [entry(avoid, use)], names=names)
    assert result.text == text
    assert result.dropped and result.reason == reason
    assert result.edits == ()


def test_any_unsafe_edit_rolls_back_an_earlier_safe_edit():
    text = "슬롯을 보고 12개를 확인했습니다. 나머지 문장은 충분히 길게 남겨 둡니다."
    result = checks.check_reply(text, [entry("슬롯", "자리"), entry("12개", "13개")], names=())
    assert result.text == text
    assert result.reason == "number"


@pytest.mark.parametrize(("padding", "dropped"), [(" " * 7, False), (" " * 6, True)])
def test_change_budget_allows_thirty_percent_but_no_more(padding, dropped):
    text = "가나다" + padding
    result = checks.check_reply(text, [entry("가나다", "라마바")], names=())
    assert result.dropped is dropped
    assert result.text == (text if dropped else "라마바" + padding)
    assert result.reason == ("change_ratio" if dropped else None)


def test_expansion_counts_inserted_characters_against_the_original_length():
    text = "칸 " + " " * 8
    result = checks.check_reply(text, [entry("칸", "아주 긴 새 단어")], names=())
    assert result.text == text and result.reason == "change_ratio"


def test_missing_name_inventory_keeps_the_original():
    text = "슬롯을 보고 나머지 설명도 함께 살펴봅니다."
    result = checks.check_reply(text, [entry("슬롯", "자리")])
    assert result.text == text and result.reason == "names_unavailable"


def test_exception_at_word_source_returns_original_without_private_error_text(monkeypatch):
    def broken():
        raise RuntimeError("synthetic private detail")

    monkeypatch.setattr(checks.words, "word_list_entries", broken)
    result = checks.check_reply("원문은 그대로 보냅니다.", names=())
    assert result.text == "원문은 그대로 보냅니다."
    assert result.dropped and result.reason == "check_error"
    assert "synthetic private detail" not in repr(result)


def test_unrecognized_nouns_and_empty_text_are_unchanged():
    assert checks.check_reply("사과", [], names=()).text == "사과"
    assert checks.check_reply("", [], names=()).text == ""


def test_conflicting_active_entries_return_original():
    text = "슬롯을 보고 나머지 설명도 함께 살펴봅니다."
    result = checks.check_reply(text, [entry("슬롯", "자리"), entry("슬롯", "공간")], names=())
    assert result.text == text and result.reason == "check_error"


def test_catalog_examples_are_present_and_flagged():
    for item in checks.calque_catalog():
        assert item.before and item.after and item.before != item.after
        spans = checks.calque_spans(item.before)
        assert any(item.before[start:end] == item.phrase for start, end in spans)
        assert checks.calque_spans(item.after) == []


def test_calque_density_counts_occurrences_and_is_silent_below_gate():
    phrase = checks.calque_catalog()[0].phrase
    at_gate = (phrase + " ") * 2
    at_gate += " " * (100 - len(at_gate))
    assert len(checks.calque_spans(at_gate)) == 2
    assert checks.calque_spans(at_gate + " ") == []


@pytest.mark.parametrize(("kind", "length", "family", "runs"), [
    ("reply", 1200, "family-b", False),
    ("report", 999, "family-b", False),
    ("report", 1000, "family-a", False),
    ("report", 1000, "family-b", True),
])
def test_reader_only_advises_on_long_reports_from_another_family(kind, length, family, runs):
    document = checks.Document("가" * length, kind, "family-a")
    result = checks.read_report(document, checks.StubReader(family))
    assert result.text == document.text
    assert result.ran is runs
    assert bool(result.notes) is runs
