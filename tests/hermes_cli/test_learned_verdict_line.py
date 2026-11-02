import json

from tests.hermes_cli._learned_env import LearnedSandbox


class VerdictLineTests(LearnedSandbox):
    def setUp(self):
        super().setUp()
        self.settings["send_target"] = "telegram:fixture"
        self.write_config()

    def verdict(self, *args):
        return self.run_tool("learned", "verdict", "--day", "2026-01-02", *args)

    def test_line_number_resolves_through_the_sent_card_after_new_items_arrive(self):
        self.write_items([self.row("r1", "rule"), self.row("m1")])
        self.assertEqual(self.run_tool("learned", "card", "--day", "2026-01-02", "--send").returncode, 0)
        sent = json.loads((self.state / "cards/2026-01-02.json").read_text())["lines"]
        self.assertEqual([line["ids"] for line in sent], [["r1"], ["m1"]])
        # Collection adds a higher-ranked item to the same day after the card went out.
        self.write_items([self.row("r1", "rule"), self.row("m1"),
                          self.row("r0", "rule", flags=["no_basis", "regressed"])])
        view = self.view()
        self.assertNotEqual(view["card_order"][1], sent[1]["id"])
        result = self.verdict("--line", "2", "drop")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([(row["item"], row["n"], row["verdict"], row["parsed_by"]) for row in self.verdict_rows()],
                         [("m1", 2, "drop", "bot")])
        view = self.view()
        self.assertEqual(view["sent_lines"], sent)
        self.assertEqual({row["id"]: row["status"] for row in view["items"]}["r1"], "new")

    def test_unsent_day_or_missing_line_exits_3_and_records_nothing(self):
        self.write_items([self.row("r1", "rule")])
        self.assertEqual(self.verdict("--line", "1", "ok").returncode, 3)
        self.assertNotIn("sent_lines", self.view())
        self.sent_card("2026-01-02", [["r1"]])
        for line in ("0", "2"):
            with self.subTest(line=line):
                self.assertEqual(self.verdict("--line", line, "ok").returncode, 3)
        self.assertNotEqual(self.verdict("--line", "1", "fix").returncode, 0)
        self.assertEqual(self.verdict_rows(), [])

    def test_line_number_keeps_a_shared_item_drop_like_reply(self):
        self.sent_card("2026-01-02", [["d1"], ["d1", "m1"]])
        self.assertEqual(self.run_tool("learned", "reply", "1 지움").returncode, 0)
        self.assertEqual(self.verdict("--line", "2", "ok").returncode, 0)
        latest = {row["item"]: row["verdict"] for row in self.verdict_rows()}
        self.assertEqual(latest, {"d1": "drop", "m1": "ok"})
        self.assertEqual(self.verdict("--line", "2", "fix", "--text", "y").returncode, 3)

    def latest(self):
        return {row["item"]: (row["verdict"], row.get("text")) for row in self.verdict_rows()}

    def test_later_line_verdict_replaces_the_same_lines_earlier_fix(self):
        self.sent_card("2026-01-02", [["d1"], ["d1", "m1"]])
        for first in (("learned", "verdict", "--day", "2026-01-02", "--line", "1", "fix", "--text", "a"),
                      ("learned", "reply", "1 고침: a")):
            for later, expected in ((("fix", "--text", "b"), ("fix", "b")), (("ok",), ("ok", None))):
                with self.subTest(first=first[1], later=later[0]):
                    (self.state / "verdicts.jsonl").unlink(missing_ok=True)
                    self.assertEqual(self.run_tool(*first).returncode, 0)
                    result = self.verdict("--line", "1", *later)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertNotIn("다른 줄", result.stdout)
                    self.assertEqual(self.latest()["d1"], expected)
                    self.assertEqual(self.verdict_rows()[-1]["n"], 1)

    def test_later_reply_replaces_the_same_lines_earlier_fix(self):
        self.sent_card("2026-01-02", [["d1"], ["d1", "m1"]])
        for first in (("learned", "verdict", "--day", "2026-01-02", "--line", "1", "fix", "--text", "a"),
                      ("learned", "reply", "1 고침: a")):
            for later, expected in (("1 고침: b", ("fix", "b")), ("1 ok", ("ok", None))):
                with self.subTest(first=first[1], later=later):
                    (self.state / "verdicts.jsonl").unlink(missing_ok=True)
                    self.assertEqual(self.run_tool(*first).returncode, 0)
                    result = self.run_tool("learned", "reply", "배운 것 01-02 " + later)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(self.latest()["d1"], expected)

    def test_a_different_lines_record_still_takes_precedence(self):
        self.sent_card("2026-01-02", [["d1"], ["d1", "m1"]])
        self.assertEqual(self.verdict("--line", "1", "fix", "--text", "a").returncode, 0)
        self.assertEqual(self.verdict("--line", "1", "fix", "--text", "b").returncode, 0)
        result = self.verdict("--line", "2", "ok")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("1건은 다른 줄 판정 유지", result.stdout)
        self.assertEqual(self.latest(), {"d1": ("fix", "b"), "m1": ("ok", None)})
        self.assertEqual(self.verdict("--line", "2", "fix", "--text", "c").returncode, 3)
        self.assertEqual(self.verdict("--line", "1", "ok").returncode, 0)
        self.assertEqual(self.latest()["d1"], ("ok", None))
