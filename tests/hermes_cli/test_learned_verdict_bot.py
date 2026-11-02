from tests.hermes_cli._learned_env import LearnedSandbox


class VerdictBotTests(LearnedSandbox):
    def setUp(self):
        super().setUp()
        self.lines = self.sent_card("2026-01-02", [["i1"], ["i2a", "i2b"], ["i3"]])

    def verdict(self, *args):
        return self.run_tool("learned", "verdict", *args)

    def test_item_verdict_is_recorded_as_bot_parsed(self):
        result = self.verdict("i3", "drop")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(result.stdout.splitlines()), 1)
        (row,) = self.verdict_rows()
        self.assertEqual({k: row[k] for k in ("day", "n", "item", "verdict", "parsed_by")},
                         {"day": "2026-01-02", "n": 3, "item": "i3", "verdict": "drop", "parsed_by": "bot"})
        self.assertNotIn("text", row)
        self.assertIsInstance(row["at"], str)

    def test_line_id_records_every_item_with_text(self):
        result = self.verdict(self.lines[1]["id"], "fix", "--text", "회의는 목요일")
        self.assertEqual(result.returncode, 0, result.stderr)
        rows = self.verdict_rows()
        self.assertEqual([(r["item"], r["n"], r["verdict"], r["text"], r["parsed_by"]) for r in rows],
                         [("i2a", 2, "fix", "회의는 목요일", "bot"), ("i2b", 2, "fix", "회의는 목요일", "bot")])

    def test_ok_after_reply_is_the_latest_verdict(self):
        self.assertEqual(self.run_tool("learned", "reply", "1 지움").returncode, 0)
        self.assertEqual(self.verdict("i1", "ok").returncode, 0)
        self.assertEqual([(r["verdict"], r["parsed_by"]) for r in self.verdict_rows()],
                         [("drop", "reply"), ("ok", "bot")])

    def test_rejected_verdicts_record_nothing(self):
        for args in (("i1", "fix"), ("missing", "ok"), ("i1", "maybe"), ("i1", "fix", "--text", " ")):
            with self.subTest(args=args):
                result = self.verdict(*args)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.verdict_rows(), [])
