"""Synthetic legacy memory shapes for migration and its real CLI proof."""

from __future__ import annotations

import sqlite3
from contextlib import closing
from pathlib import Path

from hermes_state_common import SCHEMA_SQL
from tests.hermes_cli.test_external_messages import event, message


def legacy_home(root: Path) -> Path:
    """Two profiles, branching/pruning copies, a whitespace mismatch and an LCM-only row."""
    for profile in ("alpha", "beta"):
        home = root / "profiles" / profile
        home.mkdir(parents=True)
        (home / "SOUL.md").write_text(f"Synthetic {profile} persona\n")
        (home / "memories").mkdir()
        (home / "memories" / "MEMORY.md").write_text(f"Synthetic {profile} memory\n")
        (home / "skills" / "example").mkdir(parents=True)
        (home / "skills" / "example" / "SKILL.md").write_text("Synthetic example skill\n")
        with closing(sqlite3.connect(home / "state.db")) as conn:
            conn.executescript(SCHEMA_SQL)
            conn.execute("DELETE FROM schema_version")
            conn.execute("INSERT INTO schema_version VALUES (32)")
            conn.execute("CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, "
                         "content TEXT, timestamp REAL, message_uid TEXT, active INTEGER, compacted INTEGER)")
            for session in ("session-a", "branch-a"):
                conn.execute("INSERT INTO sessions(id,source,started_at) VALUES (?, 'cli', 1767323045)",
                             (session,))
            rows = [
                (1, "session-a", "user", f"{profile} hello", 1767323045, "original", 1, 0),
                (2, "branch-a", "user", f"{profile} hello", 1767323045, "original", 1, 0),
                (3, "session-a", "tool", "full tool response", 1767323046, "tool-copy", 0, 1),
                (4, "branch-a", "tool", "short tool response", 1767323046, "tool-copy", 1, 0),
                (5, "session-a", "assistant", "with  whitespace", 1767323047, "space", 1, 0),
            ]
            conn.executemany("INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?, ?, ?)", rows)
            conn.commit()
        lcm_fixture(home / "lcm.db", profile)
    return root


def lcm_fixture(path: Path, profile: str) -> None:
    with closing(sqlite3.connect(path)) as conn:
        conn.executescript("""
            CREATE TABLE messages (store_id INTEGER PRIMARY KEY, session_id TEXT NOT NULL,
                source TEXT, conversation_id TEXT, role TEXT NOT NULL, content TEXT, timestamp REAL NOT NULL);
            CREATE TABLE summary_nodes (node_id INTEGER PRIMARY KEY, session_id TEXT NOT NULL,
                depth INTEGER, summary TEXT, token_count INTEGER, source_token_count INTEGER,
                source_ids TEXT, source_type TEXT, created_at REAL, earliest_at REAL, latest_at REAL,
                expand_hint TEXT);
        """)
        conn.executemany("INSERT INTO messages VALUES (?, 'session-a', 'agent', 'conversation-a', ?, ?, ?)", [
            (1, "user", f"{profile} hello", 1767323045),
            (2, "assistant", "with whitespace", 1767323047),
            (3, "assistant", "LCM only", 1767323048),
        ])
        conn.executemany("INSERT INTO summary_nodes VALUES (?, 'session-a', ?, ?, 5, 10, ?, ?, "
                         "1767323050,1767323045,1767323048,'')", [
                             (1, 0, "leaf", "[1,2,3]", "messages"),
                             (2, 1, "parent", "[1]", "nodes"),
                         ])
        conn.commit()


def spool_fixture(directory: Path) -> Path:
    directory.mkdir(parents=True)
    spool = directory / "events.jsonl"
    spool.write_bytes(event(message()) + event(message(), number=2)
                      + event(message(text="edited synthetic text"), number=3)
                      + event(message("line", "second-room"), number=4))
    return spool
