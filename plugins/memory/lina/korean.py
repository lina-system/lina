"""The owner's words for the concepts of one turn, carried in found memory (lina#380).

Found memory is the per-turn part of a request (the ``memory_recall`` tool result, after the cached prefix), so these
lines never change the system prompt. Only an agent whose language setting is Korean gets them
(``agent/korean_rule_section.py``); the frozen rule there is the same for every turn, and these lines are the turn's:

- each word-list entry (lina#378) whose avoid or use word is in the turn;
- for each English term in a Korean turn (at most ``MAX_ENGLISH_TERMS``), the owner's own Korean word for it
  (lina#379), when he has one;
- for an English document, a hint to look its terms up with ``owner_terms`` before writing to him.

A turn that names none of these adds nothing.
"""

from __future__ import annotations

import logging
import re
from typing import Any, Callable

logger = logging.getLogger(__name__)

HEADING = "주인의 말 (이번 턴에 나온 개념):"
LOOKUP_HINT = ("- 이번 자료는 영어 문서다. 주인에게 한국어로 쓰기 전에 개념마다 owner_terms 도구로 주인이 쓴 말을 "
               "찾는다. 찾지 못하면 새 이름을 만들지 말고 쉬운 말로 풀어 쓴다.")
MAX_ENGLISH_TERMS = 3
# A text with at least this many English words, more English letters than Hangul, is an English document.
DOCUMENT_WORDS = 40

_ENGLISH_RUN = re.compile(r"(?<![A-Za-z])[A-Za-z][A-Za-z-]*(?:[ ]+[A-Za-z][A-Za-z-]*)*")
_ENGLISH_WORD = re.compile(r"[A-Za-z]{2,}")
_HANGUL = re.compile(r"[가-힣]")


def _english_document(text: str) -> bool:
    return (len(_ENGLISH_WORD.findall(text)) >= DOCUMENT_WORDS
            and sum(c.isascii() and c.isalpha() for c in text) > len(_HANGUL.findall(text)))


def _english_terms(text: str) -> list[str]:
    """The English runs of a Korean text, one to three words each, in order, each once."""
    found: list[str] = []
    for match in _ENGLISH_RUN.finditer(text):
        term = " ".join(match.group().split())
        if len(term.split()) <= 3 and len(term) >= 3 and term.lower() not in {t.lower() for t in found}:
            found.append(term)
    return found[:MAX_ENGLISH_TERMS]


def _word_list_lines(query: str) -> list[str]:
    import psycopg

    from hermes_cli.korean import words
    from hermes_state_store import StoreUnavailableError
    try:
        entries = words.word_list_entries([query])
    except (StoreUnavailableError, psycopg.Error) as exc:
        logger.warning("lina-memory could not read the word list; this turn carries no entries from it: %s", exc)
        return []
    return [f"- '{entry.avoid}' 말고 '{entry.use}'" for entry in entries]


def _owner_term_lines(message_store: Callable[[], Any], query: str) -> list[str]:
    terms = _english_terms(query) if _HANGUL.search(query) else []
    if not terms:
        return []
    from pm.extras import available
    if not available("korean"):  # the turn never installs Kiwi; the owner_terms tool does on first use
        logger.info("lina-memory leaves the owner's terms out of this turn: the 'korean' extra is not installed")
        return []
    import psycopg

    from hermes_cli.korean.lookup import KoreanUnavailable, owner_terms, preferred_term
    from hermes_state_store import StoreUnavailableError
    store = message_store()
    lines = []
    for term in terms:
        try:
            found = owner_terms(store, term)
        except (KoreanUnavailable, StoreUnavailableError, psycopg.Error) as exc:
            logger.warning("lina-memory could not look up the owner's terms; this turn carries none: %s", exc)
            return lines
        use = preferred_term(found)
        if use:
            lines.append(f"- {term}: 주인의 말은 '{use}'")
    return lines


def turn_block(message_store: Callable[[], Any], query: str) -> str:
    """The turn's lines under ``HEADING``, or ``""`` for a non-Korean agent or a turn that names none of them.
    *message_store* gives this agent's ``MessageStore``; it is asked only for an owner-term lookup."""
    from agent.korean_rule_section import korean_agent
    if not query or not korean_agent():
        return ""
    lines = _word_list_lines(query)
    lines += [LOOKUP_HINT] if _english_document(query) else _owner_term_lines(message_store, query)
    return "\n".join([HEADING, *lines]) if lines else ""
