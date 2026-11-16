"""The shared owner profile (lina#467): one ``OWNER.md`` per install that every agent's prompt
carries before its own ``USER.md``. This module holds the matching rule both the memory tool's
duplicate refusal and ``lina memory profile merge`` use: two entries are the same fact when their
keys are equal (case, spacing, quote/dash typography, Unicode form and trailing punctuation
folded), and near the same when their keys are at least ``NEAR_DUPLICATE_RATIO`` alike by
``difflib`` (a plain, deterministic character ratio; no model call)."""

import difflib
import re
import unicodedata
from typing import Optional

from tools.memory_tool_store import _normalize_for_match

SHARED_FILENAME = "OWNER.md"
NEAR_DUPLICATE_RATIO = 0.85
_TRAILING_PUNCT = re.compile(r"[\s.!?;:,\u3002\uff01\uff1f\uff0e]+$")


def entry_key(text: str) -> str:
    """The form two entries are compared in: NFC, case-folded, typography and spacing folded,
    trailing sentence punctuation dropped."""
    folded = _normalize_for_match(unicodedata.normalize("NFC", str(text))).casefold()
    return _TRAILING_PUNCT.sub("", folded)


def similarity(a: str, b: str) -> float:
    return difflib.SequenceMatcher(None, a, b, autojunk=False).ratio()


def find_shared_match(shared_entries: list[str], content: str) -> Optional[tuple[str, str]]:
    """``(shared entry, "exact" | "near")`` for the shared entry *content* repeats, else None.
    An exact key match wins over the closest near one."""
    key = entry_key(content)
    if not key:
        return None
    best: Optional[tuple[float, str]] = None
    for entry in shared_entries:
        other = entry_key(entry)
        if other == key:
            return entry, "exact"
        ratio = similarity(key, other)
        if ratio >= NEAR_DUPLICATE_RATIO and (best is None or ratio > best[0]):
            best = (ratio, entry)
    return (best[1], "near") if best else None
