"""Source connections are read-only and diagnostics do not expose source text."""
from unittest.mock import patch
import sqlite3
from tests.hermes_cli._learned_env import LearnedSandbox

class LoadingTests(LearnedSandbox):
    def test_stage_failure_reports_only_stage_and_exception(self):
        hidden = "private source path"
        from hermes_cli.learned.collector import Collector
        for stage, exception in (("skills", ValueError), ("conclusions", KeyError),
                                 ("ledger", sqlite3.OperationalError), ("snapshots", OSError)):
            with self.subTest(stage=stage), patch.object(Collector, stage, side_effect=exception(hidden)):
                result = self.collect(ok=False)
                self.assertEqual(result.returncode, 1)
                self.assertIn(stage + ": " + exception.__name__, result.stderr)
                self.assertNotIn(hidden, result.stderr)
                self.assertFalse((self.state / "cursor.json").exists())

    def test_sources_are_read_only_through_session_db(self):
        from hermes_cli.learned.messages import Messages
        with Messages(self.profiles, 0) as messages:
            for db in messages.dbs.values():
                self.assertTrue(db.read_only)
                with self.assertRaises(sqlite3.OperationalError):
                    db._conn.execute("CREATE TABLE forbidden(id INT)")

    def test_collection_preserves_source_bytes_and_mtimes(self):
        self.memory("Synthetic baseline")
        paths = [p for p in self.hermes.rglob("*") if p.is_file()]
        before = {p: (p.read_bytes(), p.stat().st_mtime_ns) for p in paths}
        self.collect()
        self.assertEqual(before, {p: (p.read_bytes(), p.stat().st_mtime_ns) for p in paths})
        self.assertEqual(self.send_calls, [])
