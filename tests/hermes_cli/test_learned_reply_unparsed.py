from tests.hermes_cli._learned_env import LearnedSandbox

UNPARSED = ["고마워", "1 빼고 다 ok", "7 ok", "0 지움", "1 ok, 1 지움", "2 고침:", "4-2 ok",
            "1 ok, 그리고 저거도", "", "배운 것 01-02", "다 별로"]


class ReplyUnparsedTests(LearnedSandbox):
    def setUp(self):
        super().setUp()
        self.sent_card("2026-01-02", [["i1"], ["i2"], ["i3"], ["i4"], ["i5"], ["i6"]])

    def test_unparsed_reply_exits_3_and_records_nothing(self):
        for text in UNPARSED:
            with self.subTest(text=text):
                result = self.run_tool("learned", "reply", text)
                self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
                self.assertEqual(result.stdout, "")
                self.assertFalse((self.state / "verdicts.jsonl").exists())

    def test_a_valid_part_does_not_rescue_an_unparsed_part(self):
        result = self.run_tool("learned", "reply", "1 ok\n이건 뭐지")
        self.assertEqual(result.returncode, 3)
        self.assertEqual(self.verdict_rows(), [])

    def test_a_line_verdict_that_records_nothing_prints_no_success_line(self):
        self.sent_card("2026-01-02", [["d1"], ["d1", "m1"]])
        self.assertEqual(self.run_tool("learned", "verdict", "--day", "2026-01-02", "--line", "2", "drop").returncode, 0)
        before = self.verdict_rows()
        result = self.run_tool("learned", "verdict", "--day", "2026-01-02", "--line", "1", "ok")
        self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
        self.assertEqual(result.stdout, "")
        self.assertEqual(self.verdict_rows(), before)

    def test_no_sent_card_records_nothing(self):
        (self.state / "cards/2026-01-02.json").unlink()
        result = self.run_tool("learned", "reply", "다 ok")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertEqual(self.verdict_rows(), [])
