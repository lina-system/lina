from tests.hermes_cli._learned_env import LearnedSandbox

FUTURE = "2099-01-01T00:00:00+00:00"


class ReplyPickTests(LearnedSandbox):
    """Without a header, a reply goes to the latest sent card with lines, answered or not."""

    def reply(self, text):
        return self.run_tool("learned", "reply", text)

    def recorded(self):
        return [(row["day"], row["n"], row["verdict"]) for row in self.verdict_rows()]

    def test_second_piece_goes_to_the_same_card(self):
        self.sent_card("2026-01-02", [["a1"], ["a2"], ["a3"]], sent_at=FUTURE)
        self.assertEqual(self.reply("1 ok").returncode, 0)
        result = self.reply("2 지움")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout, "배운 것 01-02: 2 지움\n")
        self.assertEqual(self.recorded(), [("2026-01-02", 1, "ok"), ("2026-01-02", 2, "drop")])

    def test_older_fully_answered_card_yields_to_the_newer_one(self):
        self.sent_card("2026-01-02", [["a1"], ["a2"]], sent_at=FUTURE)
        self.assertEqual(self.reply("배운 것 01-02 다 ok").returncode, 0)
        self.sent_card("2026-01-03", [["b1"], ["b2"], ["b3"]], sent_at=FUTURE)
        self.assertEqual(self.reply("1 ok").returncode, 0)
        result = self.reply("3 지움")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.recorded()[-2:], [("2026-01-03", 1, "ok"), ("2026-01-03", 3, "drop")])

    def test_fully_answered_latest_card_is_still_picked(self):
        self.sent_card("2026-01-02", [["a1"], ["a2"]], sent_at=FUTURE)
        self.sent_card("2026-01-03", [["b1"], ["b2"]], sent_at=FUTURE)
        self.assertEqual(self.reply("배운 것 01-02 다 ok").returncode, 0)
        self.assertEqual(self.reply("다 ok").returncode, 0)
        result = self.reply("2 지움")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout, "배운 것 01-03: 2 지움\n")
        self.assertEqual(self.recorded()[-1], ("2026-01-03", 2, "drop"))

    def test_newer_answered_card_wins_over_older_open_card(self):
        self.sent_card("2026-01-02", [["a1"], ["a2"]], sent_at=FUTURE)
        self.sent_card("2026-01-03", [["b1"], ["b2"]], sent_at=FUTURE)
        self.assertEqual(self.reply("다 ok").returncode, 0)
        result = self.reply("2 지움")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout, "배운 것 01-03: 2 지움\n")
        self.assertEqual(self.recorded()[-1], ("2026-01-03", 2, "drop"))
        self.assertNotIn("2026-01-02", {row["day"] for row in self.verdict_rows()})

    def test_no_sent_card_is_still_no_card(self):
        result = self.reply("1 ok")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertEqual(self.verdict_rows(), [])
