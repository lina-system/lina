import json

from tests.hermes_cli._learned_env import LearnedSandbox


class CarryTests(LearnedSandbox):
    def test_yesterday_fix_drop_latest_status_once_not_silent_ok_or_older(self):
        self.write_items([self.row("a", "rule")])
        events = [dict(item="old", action="fix", at="2025-12-31T12:00:00+00:00", status="applied"),
                  dict(item="one", action="fix", at="2026-01-01T12:00:00+00:00", status="fix"),
                  dict(item="one", status="applied", at="2026-01-01T13:00:00+00:00"),
                  dict(item="two", action="drop", at="2026-01-01T13:00:00+00:00", status="failed"),
                  dict(item="three", action="ok", at="2026-01-01T13:00:00+00:00", status="ok"),
                  dict(item="four", status="silent", at="2026-01-01T13:00:00+00:00")]
        path = self.state / "verdicts.jsonl"
        path.write_text("".join(json.dumps(event) + "\n" for event in events))
        first = self.card()
        self.assertIn("어제 고침·지움 1건 반영됨 / 1건 아직", first.splitlines()[0])
        self.assertEqual(first, self.card())
        self.assertNotIn("어제", self.card("2026-01-03"))
        self.assertEqual(self.view()["carry"], {"applied": 1, "pending": 1})
        self.write_items([], "2026-01-02")
        quiet = self.card()
        self.assertEqual(len(quiet.splitlines()), 1)
        self.assertIn("1건 반영됨 / 1건 아직", quiet)
