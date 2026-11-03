"""FTS5 MATCH queries on the store (lina#16): SQLite's FTS5 query grammar checked as FTS5 checks it, and the query
turned into a Postgres tsquery expression. Used by ``lcm_x_store``'s translator."""

from __future__ import annotations

import re
import sqlite3
from collections.abc import Collection
from typing import Any


_CJK = re.compile(r"[\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uac00-\ud7af\uff00-\uffef]")
_QUERY_TOKEN = re.compile(r'"[^"]*"|\(|\)|[^\s()"]+')


def _fts_terms(query: str) -> list[tuple[str, str]]:
    """``(op, term)`` pairs: op ``and``/``or``/``not`` joining the term to what came before."""
    tokens = _QUERY_TOKEN.findall(query or "")
    out: list[tuple[str, str]] = []
    op = "and"
    for tok in tokens:
        if tok in ("AND", "(", ")"):
            continue
        if tok in ("OR", "NOT"):
            op = tok.lower()
            continue
        if tok.startswith("NEAR"):
            continue
        out.append((op, tok))
        op = "and"
    return out


_FTS5_PUNCT = frozenset("(){}:,+*-^")


def _fts5_tokens(query: str) -> list[tuple[str, str]]:
    """FTS5's query tokens (``kind``, ``text``): string, bareword, or one punctuation character."""
    out, i, n = [], 0, len(query)
    while i < n:
        c = query[i]
        if c.isspace():
            i += 1
        elif c == '"':
            j, buf = i + 1, []
            while True:
                if j >= n:
                    out.append(("error", "unterminated string"))  # raised when the parser reaches it, as FTS5 does
                    return out
                if query[j] == '"':
                    if j + 1 < n and query[j + 1] == '"':
                        buf.append('"')
                        j += 2
                        continue
                    break
                buf.append(query[j])
                j += 1
            out.append(("string", "".join(buf)))
            i = j + 1
        elif c.isascii() and not (c.isalnum() or c in "_\x1a"):
            out.append((c if c in _FTS5_PUNCT else "bad", c))
            i += 1
        else:
            j = i
            while j < n and not query[j].isspace() and query[j] != '"' and not (
                    query[j].isascii() and not (query[j].isalnum() or query[j] in "_\x1a")):
                j += 1
            out.append(("word", query[i:j]))
            i = j
    return out


def fts5_check(query: str, columns: Collection[str] = ("content",)) -> None:
    """Raise what SQLite's FTS5 raises for a MATCH query it cannot parse (LCM-X retries or falls back on it):
    a syntax error, an unterminated string, a column filter naming no column of the table, a special query."""
    tokens = _fts5_tokens(query)
    pos = 0
    known = {c.lower() for c in columns}

    def peek(ahead: int = 0) -> tuple[str, str]:
        token = tokens[pos + ahead] if pos + ahead < len(tokens) else ("eof", "")
        if ahead == 0 and token[0] == "error":
            raise sqlite3.OperationalError(token[1])
        return token

    def fail() -> None:
        raise sqlite3.OperationalError(f'fts5: syntax error near "{peek()[1]}"')

    def take(kind: str) -> None:
        nonlocal pos
        if peek()[0] != kind:
            fail()
        pos += 1

    def column(name: str) -> None:
        if name.lower() not in known:
            raise sqlite3.OperationalError(f"no such column: {name}")

    def is_op(word: str) -> bool:
        return peek() == ("word", word)

    def starts_nearset() -> bool:
        kind, text = peek()
        return kind in ("string", "^", "-", "{") or (kind == "word" and text not in ("AND", "OR", "NOT"))

    def term(first: bool = True) -> bool:
        """One term; True when it is bare (no ``^`` before it, no ``*`` after)."""
        nonlocal pos
        bare = True
        if first and peek()[0] == "^":
            pos += 1
            bare = False
        kind, text = peek()
        if kind not in ("string", "word") or (kind == "word" and text in ("AND", "OR", "NOT")):
            fail()
        pos += 1
        if peek()[0] == "*":
            pos += 1
            bare = False
        return bare

    def phrase(caret: bool = True) -> bool:
        nonlocal pos
        bare = term(first=caret)
        while peek()[0] == "+":
            pos += 1
            term(first=False)
            bare = False
        return bare

    def colset() -> bool:
        """A column filter before a phrase, if one is there."""
        nonlocal pos
        kind, text = peek()
        if kind == "-":
            pos += 1
            kind, text = peek()
            if kind == "string" or (kind == "word" and text not in ("AND", "OR", "NOT")):
                pos += 1
                peek()  # FTS5 names the column after reading the next token
                column(text)
                take(":")
                return True
            if kind != "{":
                fail()
        if kind == "{":
            pos += 1
            while peek()[0] == "string" or (peek()[0] == "word" and peek()[1] not in ("AND", "OR", "NOT")):
                name = peek()[1]
                pos += 1
                peek()
                column(name)
            take("}")
            take(":")
            return True
        if (kind == "string" or (kind == "word" and text not in ("AND", "OR", "NOT"))) and peek(1)[0] == ":":
            pos += 2
            column(text)
            return True
        return False

    def nearset() -> bool:
        nonlocal pos
        if peek() == ("word", "NEAR") and peek(1)[0] == "(":
            pos += 2
            phrase(caret=False)
            while peek()[0] in ("string", "word", "^"):
                phrase(caret=False)
            if peek()[0] == ",":
                pos += 1
                kind, text = peek()
                if kind != "word" or text in ("AND", "OR", "NOT"):
                    fail()
                if text == "NEAR":
                    pos += 1
                    fail()
                if not text.isdigit():
                    raise sqlite3.OperationalError(f'expected integer, got "{text}"')
                pos += 1
            take(")")
            return False
        return phrase()

    def unit() -> None:
        nonlocal pos
        if peek()[0] == "(":
            pos += 1
            expr_or()
            take(")")
            if starts_nearset() or peek()[0] == "(":
                fail()
            return
        first = peek()[1]
        leading = True
        while True:
            if colset() and peek()[0] == "(" and not leading:
                fail()  # after a phrase, FTS5 filters a phrase by column, not a group
            leading = False
            if peek()[0] == "(":
                pos += 1
                expr_or()
                take(")")
                bare = False
            else:
                bare = nearset()
            if peek()[0] == "(":
                # A group after a phrase list: FTS5 fails at once after a ^, * or + phrase; after a bare term it
                # reads bare terms on and fails after them, naming the list's first token on a ')'.
                if not bare:
                    fail()
                pos += 1
                while peek()[0] == "string" or (peek()[0] == "word" and peek()[1] not in ("AND", "OR", "NOT")):
                    pos += 1
                    if peek()[0] == "*":
                        pos += 1
                    if peek()[0] == "(":
                        fail()
                if peek()[0] == ")":
                    raise sqlite3.OperationalError(f'fts5: syntax error near "{first}"')
                fail()
            if not starts_nearset():
                return

    def expr_not() -> None:
        nonlocal pos
        unit()
        while is_op("NOT"):
            pos += 1
            unit()

    def expr_and() -> None:
        nonlocal pos
        expr_not()
        while is_op("AND"):
            pos += 1
            expr_not()

    def expr_or() -> None:
        nonlocal pos
        expr_and()
        while is_op("OR"):
            pos += 1
            expr_and()

    special = re.match(r"\s*\*\s*(\S*)", query)
    if special:
        raise sqlite3.OperationalError(f"unknown special query: {special.group(1)}")
    expr_or()
    if pos < len(tokens):
        fail()


def tsquery_sql(query: str, config: str = "simple", columns: Collection[str] = ("content",)) -> tuple[str, list[Any]]:
    """A Postgres tsquery expression (the text search *config*uration) for an FTS5 query, and its parameters; a
    query FTS5 refuses raises as FTS5 does (*columns*: the FTS table's columns, for its column filters)."""
    fts5_check(query, columns)
    sql, params = "", []
    cfg = f"'{config}'::regconfig"
    for op, term in _fts_terms(query):
        if term.startswith('"'):
            piece, value = f"phraseto_tsquery({cfg}, %s)", term.strip('"')
        elif term.endswith("*") and len(term) > 1:
            lexeme = term[:-1].lower().replace("'", "''").replace("\\", "\\\\")
            piece, value = f"to_tsquery({cfg}, %s)", f"'{lexeme}':*"
        else:
            piece, value = f"plainto_tsquery({cfg}, %s)", term
        if not sql:
            sql = piece if op != "not" else f"!!{piece}"
        else:
            sql = f"({sql}) {'||' if op == 'or' else '&&'} {'!!' if op == 'not' else ''}{piece}"
        params.append(value)
    return (sql or "plainto_tsquery('simple', '')"), params
