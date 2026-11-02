from datetime import datetime, UTC
import json

from tests.hermes_cli._learned_env import LearnedSandbox
from tests.hermes_cli._learned_env import NOW


def stamp(days_ago):
    return datetime.fromtimestamp(NOW - days_ago * 86400, UTC).isoformat()


class SilentTests(LearnedSandbox):
    def setUp(self):
        super().setUp()
        self.write_items([self.row("a", "rule"), self.row("b", "rule"), self.row("c", "rule"), self.row("d", "rule")])

    def view(self, day="2026-01-02"):
        result = self.run_tool("learned", "json", "--day", day, clocked=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def statuses(self, day="2026-01-02"):
        return {row["id"]: row["status"] for row in self.view(day)["items"]}

    def test_unanswered_lines_turn_silent_after_three_days_and_never_count_as_ok(self):
        self.sent_card("2026-01-02", [["a"], ["b"], ["c"]], sent_at=stamp(3.5))
        self.assertEqual(self.run_tool("learned", "reply", "1 ok").returncode, 0)
        self.assertEqual(self.statuses(), {"a": "ok", "b": "silent", "c": "silent", "d": "new"})
        # Silence is neither OK nor a pending fix or drop on the next card.
        self.assertEqual(self.view("2026-01-03")["carry"], {"applied": 0, "pending": 0})
        self.assertNotIn("어제", self.run_tool("learned", "card", "--day", "2026-01-03", clocked=True).stdout)

    def test_lines_are_not_silent_within_three_days(self):
        self.sent_card("2026-01-02", [["a"], ["b"]], sent_at=stamp(2.5))
        self.assertEqual(self.statuses(), {"a": "new", "b": "new", "c": "new", "d": "new"})

    def test_late_answer_replaces_silence(self):
        self.sent_card("2026-01-02", [["a"], ["b"]], sent_at=stamp(10))
        self.assertEqual(self.statuses()["b"], "silent")
        self.assertEqual(self.run_tool("learned", "reply", "배운 것 01-02 2 지움").returncode, 0)
        self.assertEqual(self.statuses()["b"], "drop")

    def test_next_card_header_counts_yesterday_fix_and_drop_lines(self):
        self.sent_card("2026-01-02", [["a"], ["b", "c"], ["d"]], sent_at=stamp(5))
        self.assertEqual(self.run_tool("learned", "reply", "1 고침: 바른 내용, 2 지움").returncode, 0)
        with (self.state / "verdicts.jsonl").open("a") as out:
            out.write(json.dumps(dict(item="a", status="applied", at=stamp(1))) + "\n")
        self.write_items([self.row("e", "rule", day="2026-01-03")], "2026-01-03")
        self.assertEqual(self.view("2026-01-03")["carry"], {"applied": 1, "pending": 1})
        card = self.run_tool("learned", "card", "--day", "2026-01-03", clocked=True).stdout
        self.assertIn("어제 고침·지움 1건 반영됨 / 1건 아직", card.splitlines()[0])
