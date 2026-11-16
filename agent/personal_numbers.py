"""ID and account numbers in text, found and masked by fixed rules (lina#308).

Deterministic code, never a model step: Korean resident and foreign registration numbers, driver's licence and
passport numbers, card numbers and bank account numbers. A masked number keeps its last four characters and its
separators (``900101-1234567`` becomes ``******-***4567``), so a masked text is never matched again. Dates, times,
amounts and phone numbers are not these shapes and stay as they are. The store applies it to the rows of a job
that carries a personal-document mark (``hermes_state_personal``); the refusal of public posts uses ``find`` to
tell whether a text still carries such a number.
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from typing import Any

KEEP = 4


@dataclass(frozen=True)
class Found:
    kind: str
    start: int
    end: int


def _plausible_birth(digits: str) -> bool:
    month, day = int(digits[2:4]), int(digits[4:6])
    return 1 <= month <= 12 and 1 <= day <= 31


def _luhn(digits: str) -> bool:
    total = 0
    for index, char in enumerate(reversed(digits)):
        value = int(char)
        if index % 2:
            value = value * 2 - 9 if value > 4 else value * 2
        total += value
    return total % 10 == 0


def _registration(match: re.Match) -> bool:
    return _plausible_birth(match.group("birth"))


def _card(match: re.Match) -> bool:
    return _luhn(re.sub(r"\D", "", match.group(0)))


def _account(match: re.Match) -> bool:
    return 10 <= len(re.sub(r"\D", "", match.group(0))) <= 16


def _always(_match: re.Match) -> bool:
    return True


# Digit boundaries only: Hangul is a word character, and a number followed by a particle (...입니다) is still one.
_NOT_DIGIT_BEFORE = r"(?<!\d)(?<!\d-)"
_NOT_DIGIT_AFTER = r"(?!\d)(?!-\d)"

# (kind, pattern, check). Order matters only where two rules could take the same span: the first one wins.
_RULES: tuple[tuple[str, re.Pattern, Any], ...] = (
    # Resident or foreign registration number: birth date, then a gender/century digit 1-8 and six digits.
    ("registration", re.compile(_NOT_DIGIT_BEFORE + r"(?P<birth>\d{6})[- ]?[1-8]\d{6}" + _NOT_DIGIT_AFTER),
     _registration),
    # Driver's licence: region (2) - year (2) - serial (6) - check (2).
    ("licence", re.compile(_NOT_DIGIT_BEFORE + r"\d{2}-\d{2}-\d{6}-\d{2}" + _NOT_DIGIT_AFTER), _always),
    # Card number: groups of four (13 to 19 digits) or a plain run of 15 or 16, passing the Luhn check. Plain 13-digit
    # runs are left out: millisecond timestamps have that length.
    ("card", re.compile(_NOT_DIGIT_BEFORE + r"(?:\d{4}[- ]\d{4}[- ]\d{4}[- ]\d{1,7}|\d{15,16})" + _NOT_DIGIT_AFTER),
     _card),
    # Passport: one letter and eight digits (M12345678), or the 2021 shape M123A4567.
    ("passport", re.compile(r"(?<![A-Za-z0-9])[A-Z](?:\d{8}|\d{3}[A-Z]\d{4})(?![A-Za-z0-9])"), _always),
    # Bank account: three or four digit groups joined by hyphens, 10 to 16 digits in all. A group that starts with
    # 0 first is a phone or area code, not an account.
    ("account", re.compile(_NOT_DIGIT_BEFORE + r"[1-9]\d{1,5}-\d{2,6}-\d{2,7}(?:-\d{1,3})?" + _NOT_DIGIT_AFTER),
     _account),
    # A plain run of 10 to 16 digits counts as an account only right after a word that says so.
    ("account", re.compile(r"(?i)(?:계좌(?:번호)?|account(?:\s+(?:no\.?|number))?|acct\.?)\s*[:：]?\s*"
                           r"(?P<number>\d{10,16})(?!\d)"), _always),
)


def find(text: str) -> list[Found]:
    """Every ID or account number in *text*, in order, without overlaps."""
    if not text or not any(char.isdigit() for char in text):
        return []
    taken: list[Found] = []
    for kind, pattern, check in _RULES:
        for match in pattern.finditer(text):
            span = match.span("number") if "number" in pattern.groupindex else match.span()
            if not check(match) or any(span[0] < f.end and f.start < span[1] for f in taken):
                continue
            taken.append(Found(kind, *span))
    return sorted(taken, key=lambda f: f.start)


def _masked(number: str) -> str:
    """Every letter and digit but the last four becomes ``*``; separators stay."""
    kept = 0
    out = []
    for char in reversed(number):
        if char.isalnum():
            out.append(char if kept < KEEP else "*")
            kept += 1
        else:
            out.append(char)
    return "".join(reversed(out))


def mask_text(text: str) -> tuple[str, int]:
    """``(text with every number masked, how many were masked)``."""
    found = find(text)
    if not found:
        return text, 0
    parts, at = [], 0
    for item in found:
        parts += [text[at:item.start], _masked(text[item.start:item.end])]
        at = item.end
    parts.append(text[at:])
    return "".join(parts), len(found)


def mask_json(text: str) -> tuple[str, int]:
    """``(text masked, count)`` for *text* holding a JSON value: the decoded strings are masked and the value
    serialized again, since ``json.dumps`` escapes the non-ASCII labels (계좌번호) the account rule reads. A text that
    is not JSON is masked as it is; one with no number comes back unchanged."""
    try:
        value = json.loads(text)
    except ValueError:
        return mask_text(text)
    masked, count = mask_value(value)
    return (json.dumps(masked), count) if count else (text, 0)


def mask_value(value: Any) -> tuple[Any, int]:
    """*value* with every string inside it (lists, tuples and dicts, keys kept) masked, and the count. A string that
    holds a JSON array or object (tool call arguments) is masked by its decoded strings (``mask_json``)."""
    if isinstance(value, str):
        return mask_json(value) if value.lstrip()[:1] in ("[", "{") else mask_text(value)
    if isinstance(value, (list, tuple)):
        items = [mask_value(item) for item in value]
        return type(value)(item for item, _ in items), sum(count for _, count in items)
    if isinstance(value, dict):
        items = {key: mask_value(item) for key, item in value.items()}
        return {key: item for key, (item, _) in items.items()}, sum(count for _, count in items.values())
    return value, 0
