"""lina#703: the bind probe for legacy unnormalized conversation ids is one statement however many ids the store
holds (it was one statement per distinct id, ~60 s for 671 ids on the store), and it still finds exactly the
values today's ``str(value) != str(value).strip()`` rule calls legacy."""

import sqlite3

import pytest

# NULL, blank and whitespace-only, padded with each kind of character str.strip() removes, and look-alikes it keeps.
STORED = [None, "", "conv", "a b", "  ", " conv", "conv ", "\tconv", "conv\n", "\r\nconv\x0b", "\x0cx\x1f",
          "\x85y", "z\xa0", "\u2003w", "v\u3000", "\u200bu", "plain-1", "plain-2"]


class _CountingConnection(sqlite3.Connection):
    statements = 0

    def execute(self, sql, parameters=(), /):
        type(self).statements += 1
        return super().execute(sql, parameters)


def _messages(conn, conversation_ids, *, indexed=True):
    conn.execute("CREATE TABLE messages (store_id INTEGER PRIMARY KEY, session_id TEXT, conversation_id TEXT)")
    if indexed:
        conn.execute("CREATE INDEX idx_msg_conversation_session ON messages (conversation_id, session_id, store_id)")
    conn.executemany("INSERT INTO messages (session_id, conversation_id) VALUES ('S', ?)",
                     [(value,) for value in conversation_ids])
    conn.commit()


def _assert_legacy_matches_str_strip(conn):
    from hermes_lcm.db_bootstrap import refresh_legacy_conversation_ids

    expected: dict = {}
    for value in dict.fromkeys(STORED):
        if value is None or str(value) != str(value).strip():
            expected.setdefault(str(value or "").strip(), []).append(value)
    legacy = refresh_legacy_conversation_ids(conn)
    assert {key: sorted(map(repr, values)) for key, values in legacy.items()} == {
        key: sorted(map(repr, values)) for key, values in expected.items()}
    assert "\u200bu" not in [value for values in legacy.values() for value in values]  # not whitespace for strip()


@pytest.mark.parametrize("indexed", [True, False])
def test_the_probe_runs_the_same_statements_for_three_ids_and_for_hundreds(tmp_path, indexed):
    from hermes_lcm.db_bootstrap import refresh_legacy_conversation_ids

    counts = {}
    for n in (3, 600):
        conn = sqlite3.connect(str(tmp_path / f"ids-{n}-{indexed}.db"), factory=_CountingConnection)
        try:
            _messages(conn, [f"conv-{i:04d}" for i in range(n)] + [" padded"], indexed=indexed)
            _CountingConnection.statements = 0
            assert refresh_legacy_conversation_ids(conn) == {"padded": [" padded"]}
            counts[n] = _CountingConnection.statements
        finally:
            conn.close()
    assert counts[600] == counts[3]


def test_the_probe_on_lcm_x_s_store_returns_exactly_the_ids_str_strip_changes(tmp_path):
    """Through LCM-X's message store: SQLite's index seeks here, the store's DISTINCT scan in the Postgres run."""
    from hermes_lcm.store import MessageStore

    store = MessageStore(tmp_path / "lcm.db")
    try:
        conn = store.connection
        for value in STORED:
            store_id = store.append("S", {"role": "user", "content": "row"}, conversation_id="placeholder")
            conn.execute("UPDATE messages SET conversation_id = ? WHERE store_id = ?", (value, store_id))
        conn.commit()
        _assert_legacy_matches_str_strip(conn)
    finally:
        store.close()


def test_the_probe_without_the_conversation_index_returns_exactly_the_ids_str_strip_changes(tmp_path):
    conn = sqlite3.connect(str(tmp_path / "unindexed.db"))
    try:
        _messages(conn, STORED, indexed=False)
        _assert_legacy_matches_str_strip(conn)
    finally:
        conn.close()
