"""lina#308: the fixed rules that mask ID and account numbers. Every number here is synthetic."""

from __future__ import annotations

import pytest

from agent.personal_numbers import find, has_number, mask_text, mask_value

POSITIVES = [
    ("주민번호 900101-1234567입니다", "주민번호 ******-***4567입니다", "registration"),
    ("9001011234567", "*********4567", "registration"),
    ("외국인등록번호 850315-5123456", "외국인등록번호 ******-***3456", "registration"),
    ("면허 12-34-567890-12", "면허 **-**-****90-12", "licence"),
    ("여권 M12345678", "여권 *****5678", "passport"),
    ("passport M123A4567.", "passport *****4567.", "passport"),
    ("카드 4111 1111 1111 1111", "카드 **** **** **** 1111", "card"),
    ("card 4111-1111-1111-1111", "card ****-****-****-1111", "card"),
    ("4111111111111111", "************1111", "card"),
    ("입금 110-123-456789로", "입금 ***-***-**6789로", "account"),
    ("1002-123-456789", "****-***-**6789", "account"),
    ("123456-01-123456", "******-**-**3456", "account"),
    ("계좌번호: 12345678901234", "계좌번호: **********1234", "account"),
    ("account no. 1234567890", "account no. ******7890", "account"),
]

NEGATIVES = [
    "2026-11-16",
    "2026-11-16 10:30:00",
    "1,250,000원",
    "총 12345678원",
    "$12,345.67",
    "010-1234-5678",
    "02-1234-5678",
    "1794795060000",
    "1234 5678 9012 3456",
    "991301-1234567",
    "v1.2.3 build 20261116",
    "M1234567",
    "page 12 of 34, line 567890",
]


@pytest.mark.parametrize(("text", "masked", "kind"), POSITIVES)
def test_ids_and_accounts_keep_only_their_last_four_characters(text, masked, kind):
    assert mask_text(text) == (masked, 1)
    assert [found.kind for found in find(text)] == [kind]


@pytest.mark.parametrize("text", NEGATIVES)
def test_dates_amounts_phones_and_near_misses_are_left_alone(text):
    assert mask_text(text) == (text, 0)


def test_a_masked_text_is_not_masked_again():
    once, count = mask_text("900101-1234567, 110-123-456789")
    assert count == 2
    assert mask_text(once) == (once, 0)


def test_nested_values_are_masked_everywhere_and_keep_their_shape():
    value = ("내 번호 900101-1234567", [{"type": "text", "text": "카드 4111 1111 1111 1111"}], None, 7)
    masked, count = mask_value(value)
    assert count == 2
    assert masked == ("내 번호 ******-***4567", [{"type": "text", "text": "카드 **** **** **** 1111"}], None, 7)
    assert has_number(value) and not has_number(masked)
