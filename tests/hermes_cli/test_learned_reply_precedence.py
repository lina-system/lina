from tests.hermes_cli._learned_env import LearnedSandbox


class ReplyPrecedenceTests(LearnedSandbox):
    """A memory deletion sits on its own line 1 and inside the memory summary on line 2."""

    def setUp(self):
        super().setUp()
        self.write_items([self.row("d1"), self.row("m1"), self.row("x", "rule")])
        self.lines = self.sent_card("2026-01-02", [["d1"], ["d1", "m1"], ["x"]],
                                    sent_at="2099-01-01T00:00:00+00:00")

    def reply(self, text):
        return self.run_tool("learned", "reply", text)

    def statuses(self):
        return {row["id"]: row["status"] for row in self.view()["items"]}

    def latest(self):
        found = {}
        for row in self.verdict_rows():
            found[row["item"]] = (row["verdict"], row.get("text"))
        return found

    def test_explicit_drop_beats_rest_ok(self):
        result = self.reply("1 지움, 나머지 ok")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.statuses(), {"d1": "drop", "m1": "ok", "x": "ok"})
        self.assertEqual([row["item"] for row in self.verdict_rows()].count("d1"), 1)

    def test_explicit_drop_beats_whole_ok_and_range(self):
        for text in ("다 ok, 1 지움", "1-3 ok, 1 지움"):
            with self.subTest(text=text):
                (self.state / "verdicts.jsonl").unlink(missing_ok=True)
                self.assertEqual(self.reply(text).returncode, 0)
                self.assertEqual(self.statuses()["d1"], "drop")

    def test_explicit_drop_or_fix_beats_explicit_ok(self):
        self.assertEqual(self.reply("1 지움, 2 ok").returncode, 0)
        self.assertEqual(self.statuses(), {"d1": "drop", "m1": "ok", "x": "new"})
        (self.state / "verdicts.jsonl").unlink()
        self.assertEqual(self.reply("2 ok, 1 고침: 바른 내용").returncode, 0)
        self.assertEqual(self.latest()["d1"], ("fix", "바른 내용"))

    def test_explicit_drop_against_fix_is_ambiguous(self):
        result = self.reply("1 지움, 2 고침: x")
        self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
        self.assertEqual(result.stdout, "")
        self.assertEqual(self.verdict_rows(), [])

    def test_later_reply_still_wins(self):
        self.assertEqual(self.reply("1 지움").returncode, 0)
        self.assertEqual(self.reply("배운 것 01-02 1 ok").returncode, 0)
        self.assertEqual(self.statuses()["d1"], "ok")

    def test_bot_line_verdict_keeps_shared_item_drop(self):
        self.assertEqual(self.run_tool("learned", "verdict", self.lines[0]["id"], "drop").returncode, 0)
        result = self.run_tool("learned", "verdict", self.lines[1]["id"], "ok")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.statuses(), {"d1": "drop", "m1": "ok", "x": "new"})
        before = self.verdict_rows()
        result = self.run_tool("learned", "verdict", self.lines[1]["id"], "fix", "--text", "y")
        self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
        self.assertEqual(self.verdict_rows(), before)
        # An item id is explicit: the later record wins.
        self.assertEqual(self.run_tool("learned", "verdict", "d1", "ok").returncode, 0)
        self.assertEqual(self.statuses()["d1"], "ok")
