"""Incomplete or future curator records remain eligible for later collection."""
import json
from tests.hermes_cli._learned_env import LearnedSandbox

class LateTests(LearnedSandbox):
    def test_future_record_stops_cursor_until_ready(self):
        self.skill("old", "new", "future", at="2026-01-02T12:00:01")
        self.collect("2026-01-02T12:00:00")
        self.assertEqual(self.items(), [])
        self.collect("2026-01-02T12:00:01")
        self.assertEqual(len(self.items()), 1)
        self.collect("2026-01-02T12:00:02")
        self.assertEqual(len(self.items()), 1)

    def test_partial_line_is_not_consumed(self):
        self.skill("old", "new", "one")
        path = self.profiles["amber"] / "skills/.curator_ledger.jsonl"
        original = path.read_bytes()
        path.write_bytes(original[:-1])
        self.collect()
        self.assertEqual(self.items(), [])
        path.write_bytes(original)
        self.collect()
        self.assertEqual(len(self.items()), 1)

    def test_invalid_times_do_not_commit_cursor(self):
        self.skill("old", "new", "one")
        self.collect()
        path = self.profiles["amber"] / "skills/.curator_ledger.jsonl"
        original = path.read_text()
        before = (self.state / "cursor.json").read_bytes()
        for value in (None, "not-a-time", "nan", "inf"):
            event = json.loads(original)
            event["id"], event["ts"] = "invalid", value
            path.write_text(original + json.dumps(event) + "\n")
            self.assertEqual(self.collect(ok=False).returncode, 1)
            self.assertEqual((self.state / "cursor.json").read_bytes(), before)
