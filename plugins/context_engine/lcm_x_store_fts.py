"""FTS5 MATCH queries on the store (lina#16): SQLite's FTS5 query grammar checked as FTS5 checks it, and the query
turned into a Postgres tsquery expression. Used by ``lcm_x_store``'s translator."""

from __future__ import annotations

import re
import unicodedata
import sqlite3
from collections.abc import Collection
from typing import Any


_CJK = re.compile(r"[\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uac00-\ud7af\uff00-\uffef]")
_QUERY_TOKEN = re.compile(r'"[^"]*"|\(|\)|[^\s()"]+')


def fts_terms(query: str) -> list[tuple[str, str]]:
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


def unicode61(text: str) -> str:
    """ctx.lcm_unicode61 in Python: FTS5's unicode61 tokens (letters, digits and private-use characters, accents removed), space-separated."""
    folded = "".join(c for c in unicodedata.normalize("NFD", text) if not "\u0300" <= c <= "\u036f")
    return re.sub(r"[^\w\ue000-\uf8ff\U000f0000-\U000ffffd\U00100000-\U0010fffd]|_", " ", folded)


def tsquery_sql(query: str, config: str = "simple", columns: Collection[str] = ("content",)) -> tuple[str, list[Any]]:
    """A Postgres tsquery expression (the text search *config*uration) for an FTS5 query, and its parameters; a
    query FTS5 refuses raises as FTS5 does (*columns*: the FTS table's columns, for its column filters)."""
    fts5_check(query, columns)
    sql, params = "", []
    cfg = f"'{config}'::regconfig"
    for op, term in fts_terms(query):
        # FTS5 tokenizes a bareword or phrase as it tokenized the text: several tokens are a phrase.
        if term.endswith("*") and len(term) > 1 and not term.startswith('"'):
            words = unicode61(term[:-1]).lower().split()
            lexemes = [w.replace("'", "''").replace("\\", "\\\\") for w in words]
            value = " <-> ".join(f"'{w}'" for w in lexemes) + ":*" if lexemes else ""
            piece = f"to_tsquery({cfg}, %s)"
        else:
            piece, value = f"phraseto_tsquery({cfg}, ctx.lcm_unicode61(%s))", term.strip('"')
        if not sql:
            sql = piece if op != "not" else f"!!{piece}"
        else:
            sql = f"({sql}) {'||' if op == 'or' else '&&'} {'!!' if op == 'not' else ''}{piece}"
        params.append(value)
    return (sql or "plainto_tsquery('simple', '')"), params


SNIPPET_MARK = "\x04"


def _unicode61_tokens(text: str) -> list[tuple[int, int]]:
    """(start, end) of each unicode61 token of *text*: letters, digits, marks and private-use characters."""
    spans, start = [], None
    for i, c in enumerate(text):
        inside = unicodedata.category(c)[0] in "LNM" or unicodedata.category(c) == "Co"
        if inside and start is None:
            start = i
        elif not inside and start is not None:
            spans.append((start, i))
            start = None
    if start is not None:
        spans.append((start, len(text)))
    return spans


def fts5_snippet(source: str, open_mark: str = ">>>", close_mark: str = "<<<", ellipsis: str = "...",
                 n_token: int = 40) -> str:
    """FTS5's snippet() (fts5_aux.c) over 0007's ctx.lcm_snippet_source value: the best window of *n_token*
    tokens (1000 per phrase first seen, 1 per repeat, +100/+120 for a window that starts a sentence), with each
    coalesced phrase instance between the marks."""
    import json

    _, meta, text = source.split(SNIPPET_MARK, 2)
    info = json.loads(meta)
    if info.get("marks"):
        open_mark, close_mark, ellipsis, n_token = info["marks"]
    spans = _unicode61_tokens(text)
    lexemes = info["lex"] if len(info["lex"]) == len(spans) else [
        "".join(c for c in unicodedata.normalize("NFD", text[s:e]) if not "\u0300" <= c <= "\u036f").lower()
        for s, e in spans]
    insts = []  # (offset, phrase, size), FTS5's xInst order
    for ip, (words, prefix) in enumerate(zip(info["phr"], info["pre"] or [])):
        size = len(words)
        for i in range(len(lexemes) - size + 1 if size else 0):
            if all(lexemes[i + k] == w or (prefix and k == size - 1 and lexemes[i + k].startswith(w))
                   for k, w in enumerate(words)):
                insts.append((i, ip, size))
    insts.sort()
    n_doc = len(spans)

    def score(pos: int) -> tuple[int, int]:
        seen, total, first, last = set(), 0, -1, 0
        for off, ip, size in insts:
            if pos <= off < pos + n_token:
                total += 1 if ip in seen else 1000
                seen.add(ip)
                if first < 0:
                    first = off
                last = off + size
        adj = first - (n_token - (last - first)) // 2
        adj = n_doc - n_token if adj + n_token > n_doc else adj
        return total, max(adj, 0)

    firsts = []
    if n_doc > n_token:
        for i, (s, _e) in enumerate(spans):
            if i == 0:
                firsts.append(0)
                continue
            j = s - 1
            while j >= 0 and text[j] in " \t\n\r":
                j -= 1
            if j != s - 1 and j >= 0 and text[j] in ".:":
                firsts.append(i)
    best_score, best_start = 0, 0
    for off, _ip, _size in insts:
        sc, adj = score(off)
        if sc > best_score:
            best_score, best_start = sc, adj
        if firsts:
            jj = 0
            while jj < len(firsts) - 1 and firsts[jj + 1] <= off:
                jj += 1
            if firsts[jj] < off:
                sc = score(firsts[jj])[0] + (120 if firsts[jj] == 0 else 100)
                if sc > best_score:
                    best_score, best_start = sc, firsts[jj]

    def coalesced(k: int) -> tuple[int, int, int]:
        start = end = -1
        while k < len(insts):
            off, _ip, size = insts[k]
            if start < 0:
                start, end = off, off + size - 1
            elif off <= end:
                end = max(end, off + size - 1)
            else:
                break
            k += 1
        return start, end, k

    range_start, range_end = best_start, best_start + n_token - 1
    out = [ellipsis] if best_start > 0 else []
    it_start, it_end, k = coalesced(0)
    while 0 <= it_start < best_start:
        it_start, it_end, k = coalesced(k)
    cur, is_open = 0, False
    for pos, (s, e) in enumerate(spans):
        if pos < range_start or pos > range_end:
            continue
        if range_start and pos == range_start:
            cur = s
        if is_open and (pos <= it_start or it_start < 0) and s > cur:
            out.append(close_mark)
            is_open = False
        if pos == it_start and not is_open:
            out += [text[cur:s], open_mark]
            cur, is_open = s, True
        if pos == it_end:
            if not is_open:
                out.append(open_mark)
                is_open = True
            out.append(text[cur:e])
            cur = e
            it_start, it_end, k = coalesced(k)
        if pos == range_end:
            if is_open:
                if it_start >= 0 and pos >= it_start:
                    out.append(text[cur:e])
                    cur = e
                out.append(close_mark)
                is_open = False
            out.append(text[cur:e])
            cur = e
    if is_open:
        out.append(close_mark)
    out.append(text[cur:] if range_end >= n_doc - 1 else ellipsis)
    return "".join(out)
