import hashlib
import json
import sqlite3
import subprocess
import sys
from datetime import datetime, UTC

from pathlib import Path
import contextlib
import io
import os
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
NOW = 1800000000


def epoch(value):
    return datetime.fromisoformat(value).replace(tzinfo=UTC).timestamp()


class LearnedSandbox(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        from tests.hermes_cli._work_env import WorkEnv
        from pytest import MonkeyPatch
        self.monkeypatch = MonkeyPatch()
        self.addCleanup(self.monkeypatch.undo)
        self.work_env = WorkEnv(self.home, self.monkeypatch)
        self.home = self.work_env.home
        self.desk = self.work_env.desk  # WorkEnv made it; "desk" and "Desk" are one folder on macOS
        self.hermes = self.home / "Synthetic LINA"
        self.state = self.hermes / "learned"
        self.ledger = self.hermes / "work"
        self.env = {**os.environ, "HOME": str(self.home), "LINA_HOME": str(self.hermes),
                    "HERMES_HOME": str(self.hermes), "TZ": "UTC"}
        self.settings = {"profiles": ["default", "amber"], "send_retry_seconds": 0}
        self.send_result = None
        self.send_calls = []
        self.write_config()
        self.environ = patch.dict(os.environ, self.env)
        self.environ.start()
        self.addCleanup(self.environ.stop)
        self.sender = patch("hermes_cli.learned.send.attempt", side_effect=self._send)
        self.sender_mock = self.sender.start()
        self.addCleanup(self.sender.stop)
        self.profiles = {"default": self.hermes, "amber": self.hermes / "profiles/amber"}
        for home in self.profiles.values():
            (home / "skills").mkdir(parents=True)
            (home / "memories").mkdir()
            with sqlite3.connect(str(home / "state.db")) as db:
                db.executescript("""
                    CREATE TABLE sessions(id TEXT PRIMARY KEY, source TEXT, title TEXT, started_at REAL);
                    CREATE TABLE messages(id INTEGER PRIMARY KEY, session_id TEXT, role TEXT,
                      content TEXT, tool_call_id TEXT, tool_calls TEXT, tool_name TEXT, timestamp REAL);
                    INSERT INTO sessions VALUES ('s1', 'telegram', 'Synthetic', 0);
                """)
        self.count = 0

    def message(self, text, at="2026-01-02T10:00:00", profile="amber", session="s1", role="user"):
        self.count += 1
        with sqlite3.connect(str(self.profiles[profile] / "state.db")) as db:
            db.execute("INSERT INTO messages(id,session_id,role,content,timestamp) VALUES (?,?,?,?,?)",
                       (self.count, session, role, text, epoch(at)))

    def call(self, name, args, result=None, at="2026-01-02T11:00:00", profile="amber", session="s1"):
        self.count += 1
        call_id = "call-" + str(self.count)
        call = {"id": "different-" + str(self.count), "call_id": call_id, "type": "function",
                "function": {"name": name, "arguments": json.dumps(args)}}
        with sqlite3.connect(str(self.profiles[profile] / "state.db")) as db:
            db.execute("INSERT INTO messages(id,session_id,role,tool_calls,timestamp) VALUES (?,?,?,?,?)",
                       (self.count, session, "assistant", json.dumps([call]), epoch(at)))
            if result is not None:
                # Unrelated result appears first; attribution must use the call ID.
                self.count += 1
                db.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)",
                           (self.count, session, "tool", '{"removed_entry":"wrong entry"}',
                            "unrelated", None, "memory", epoch(at) + 1))
                self.count += 1
                db.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)",
                           (self.count, session, "tool", json.dumps(result), call_id, None, name, epoch(at) + 2))
        return call_id

    def write_config(self):
        from hermes_cli.config import atomic_config_replace
        self.hermes.mkdir(parents=True, exist_ok=True)
        doc = {"learned": self.settings, "desk": {"path": str(self.desk)},
               "work": {"locale": "ko", "owner": {"id": "owner-fixture", "name": "사용자"},
                        "agents": {"default": "알파", "amber": "호박", "lina": "하나", "sion": "둘",
                                   "kai": "셋", "rumi": "넷"}, "store": {"backend": "sqlite"}}}
        atomic_config_replace(self.hermes / "config.yaml", doc)

    def _send(self, config, text, mask):
        self.send_calls.append(text)
        return self.send_result

    def run_tool(self, name, *args, clocked=False, source=None, env=None):
        from hermes_cli.learned import main
        output, errors = io.StringIO(), io.StringIO()
        with patch.dict(os.environ, {**self.env, **(env or {})}), contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            with patch("time.time", return_value=NOW) if clocked else contextlib.nullcontext():
                try:
                    code = main(list(args))
                except SystemExit as error:
                    code = error.code
        return SimpleNamespace(returncode=code, stdout=output.getvalue(), stderr=errors.getvalue())

    def collect(self, at="2026-01-02T23:40:00", source=None, ok=True):
        with patch("time.time", return_value=epoch(at)):
            result = self.run_tool("learned", "collect")
        if ok:
            self.assertEqual(result.returncode, 0, result.stderr)
        return result

    def calls(self):
        return self.send_calls

    def items(self, day="2026-01-02", kind=None):
        path = self.state / "items" / (day + ".jsonl")
        rows = [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []
        return [row for row in rows if kind is None or row["kind"] == kind]

    def memory(self, text, name="MEMORY.md", profile="amber"):
        (self.profiles[profile] / "memories" / name).write_text(text)

    def list_config(self, name, lines):
        self.settings[{"rules.txt": "rules", "private.txt": "private"}[name]] = lines
        self.write_config()

    def blob(self, text, profile="amber"):
        digest = hashlib.sha256(text.encode()).hexdigest()
        path = self.profiles[profile] / ".curator_backups/blobs" / digest
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return digest

    def skill(self, before, after, id_, actor="curator", skill="sample", at="2026-01-02T11:00:00"):
        row = dict(id=id_, ts=at + "+00:00", actor=actor, action="patch", skill=skill,
                   evidence={"session_id": "s1"},
                   before=[{"path": skill + "/SKILL.md", "sha256": self.blob(before)}],
                   after=[{"path": skill + "/SKILL.md", "sha256": self.blob(after)}])
        with (self.profiles["amber"] / "skills/.curator_ledger.jsonl").open("a") as out:
            out.write(json.dumps(row) + "\n")

    def ledger_rows(self, *rows):
        from hermes_cli.work import store_sqlite
        db = store_sqlite.connect(self.ledger / "work.db", "owner-fixture", {}, create=True)
        try:
            for row in rows:
                db.execute("PRAGMA foreign_keys=OFF")
                db.execute("INSERT INTO event(kind, actor_id, agent_id, todo_id, data) VALUES (?, ?, ?, ?, ?)",
                           ("created", "amber", "amber", str(row["id"]), json.dumps({"ledger": json.dumps(row)})))
        finally:
            db.close()

    def write_items(self, rows, day="2026-01-02"):
        folder = self.state / "items"
        folder.mkdir(parents=True, exist_ok=True)
        (folder / (day + ".jsonl")).write_text("".join(json.dumps(row) + "\n" for row in rows))

    def row(self, id_, kind="memory", **extra):
        row = dict(id=id_, day="2026-01-02", kind=kind, bot="amber", actor="chat",
                   target=kind + ":" + id_, before="예전 내용", after="새로운 내용",
                   summary="Store change", diff="", basis=[], flags=["no_basis"],
                   source="seen", status="new", ops=1, rank=0, undo={"how": "owner"})
        row.update(extra)
        return row

    def view(self, day="2026-01-02"):
        result = self.run_tool("learned", "json", "--day", day)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def sent_card(self, day, lines, sent_at="2026-01-03T11:05:00+00:00"):
        """A delivered card's line record, as `card --send` leaves it."""
        folder = self.state / "cards"
        folder.mkdir(parents=True, exist_ok=True)
        rows = [dict(n=n, id="F" + day.replace("-", "") + str(n), ids=ids) for n, ids in enumerate(lines, 1)]
        (folder / (day + ".json")).write_text(json.dumps(dict(day=day, sent_at=sent_at, lines=rows)))
        return rows

    def verdict_rows(self):
        path = self.state / "verdicts.jsonl"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def card(self, day="2026-01-02", llm=False):
        result = self.run_tool("learned", "card", "--day", day, *(["--llm"] if llm else []))
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout
