from tests.hermes_cli._learned_env import LearnedSandbox


class QuietTests(LearnedSandbox):
    def test_unflagged_day_is_one_line_with_total_and_no_item_order(self):
        self.write_items([self.row(str(i), flags=[]) for i in range(12)])
        self.assertEqual(self.card(), "오늘 봇들이 바꾼 것 12건, 걸리는 건 없음\n")
        self.assertEqual(self.view()["card_order"], [])

    def test_empty_day_and_private_only_day(self):
        self.assertEqual(self.card(), "오늘 봇들이 바꾼 것 0건, 걸리는 건 없음\n")
        self.write_items([self.row("a", flags=["private"], after="숨긴내용")])
        self.assertEqual(len(self.card().splitlines()), 1)
        self.assertNotIn("숨긴내용", self.card())

    def test_self_report_quiet_total_preserves_source(self):
        self.write_items([self.row("a", flags=[], source="bot_said")])
        self.assertIn("봇 말 1건", self.card())
        self.assertNotIn("바꾼 것 1건", self.card())
