from tests.hermes_cli._learned_env import LearnedSandbox

FUTURE = "2099-01-01T00:00:00+00:00"


class ReplyHeaderTests(LearnedSandbox):
    def setUp(self):
        super().setUp()
        self.sent_card("2026-01-02", [["a1"], ["a2"], ["a3"]], sent_at=FUTURE)
        self.sent_card("2026-01-03", [["b1"], ["b2"], ["b3"]], sent_at=FUTURE)

    def reply(self, text):
        return self.run_tool("learned", "reply", text)

    def recorded(self):
        return [(row["day"], row["n"], row["verdict"], row.get("text")) for row in self.verdict_rows()]

    def test_header_inside_fix_text_neither_picks_a_card_nor_changes_the_text(self):
        text = "배운 것 01-02가 아니라 오늘 내용"
        result = self.reply("1 고침: " + text)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "배운 것 01-03: 1 고침\n")
        self.assertEqual(self.recorded(), [("2026-01-03", 1, "fix", text)])

    def test_quoted_header_line_picks_its_card(self):
        for quoted in ("배운 것 01-02 · 규칙 1 · 기억 2", "> 배운 것 01-02 · 규칙 1 · 기억 2", "> 배운 것 01-02"):
            with self.subTest(quoted=quoted):
                (self.state / "verdicts.jsonl").unlink(missing_ok=True)
                result = self.reply(quoted + "\n2 지움")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.recorded(), [("2026-01-02", 2, "drop", None)])

    def test_leading_header_with_verdicts_picks_its_card(self):
        result = self.reply("배운 것 01-02 다 ㅇㅋ")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.recorded(), [("2026-01-02", n, "ok", None) for n in (1, 2, 3)])
