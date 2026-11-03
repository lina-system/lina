from tests.hermes_cli._learned_env import LearnedSandbox

ALL = {n: ("ok", None) for n in range(1, 7)}


def rest_ok(**given):
    return {**ALL, **{int(n[1:]): value for n, value in given.items()}}


# Korean replies to a six-line card and the verdict each numbered line gets.
CASES = [
    ("다 ㅇㅋ", ALL),
    ("다 OK", ALL),
    ("ㅇㅋ", ALL),
    ("ok", ALL),
    ("Ok!", ALL),
    ("전부 오케이", ALL),
    ("1 ok", {1: ("ok", None)}),
    ("1번 ok", {1: ("ok", None)}),
    ("3 지움", {3: ("drop", None)}),
    ("4 지워", {4: ("drop", None)}),
    ("3 지워줘", {3: ("drop", None)}),
    ("5 빼", {5: ("drop", None)}),
    ("4 삭제", {4: ("drop", None)}),
    ("2 고침: 회의는 수요일", {2: ("fix", "회의는 수요일")}),
    ("6 고쳐 이름은 민지가 아니라 민서", {6: ("fix", "이름은 민지가 아니라 민서")}),
    ("5 수정: 금요일 저녁", {5: ("fix", "금요일 저녁")}),
    ("1-4 ok", {n: ("ok", None) for n in range(1, 5)}),
    ("1~2 ㅇㅋ", {1: ("ok", None), 2: ("ok", None)}),
    ("1, 2 OK", {1: ("ok", None), 2: ("ok", None)}),
    ("1·3·4 ok, 2 지움", {1: ("ok", None), 3: ("ok", None), 4: ("ok", None), 2: ("drop", None)}),
    ("1 ok, 2 지움", {1: ("ok", None), 2: ("drop", None)}),
    ("1 ok\n2 지움\n3 고침: 내일 오전", {1: ("ok", None), 2: ("drop", None), 3: ("fix", "내일 오전")}),
    ("1-3 ok, 4 빼, 5 고쳐 사과, 배 둘 다, 6 지워",
     {1: ("ok", None), 2: ("ok", None), 3: ("ok", None), 4: ("drop", None),
      5: ("fix", "사과, 배 둘 다"), 6: ("drop", None)}),
    ("2 지움, 나머지 ok", rest_ok(n2=("drop", None))),
    ("다 ok, 6 고침: 저녁 7시", rest_ok(n6=("fix", "저녁 7시"))),
    ("배운 것 01-02\n1 ok", {1: ("ok", None)}),
]


class ReplyGrammarTests(LearnedSandbox):
    def setUp(self):
        super().setUp()
        # Line 2 is a folded line holding two items.
        self.lines = self.sent_card("2026-01-02", [["i1"], ["i2a", "i2b"], ["i3"], ["i4"], ["i5"], ["i6"]])

    def reply(self, text):
        return self.run_tool("learned", "reply", text)

    def test_korean_replies_record_expected_verdicts(self):
        self.assertGreaterEqual(len(CASES), 20)
        for text, expected in CASES:
            with self.subTest(text=text):
                (self.state / "verdicts.jsonl").unlink(missing_ok=True)
                result = self.reply(text)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(len(result.stdout.strip().splitlines()), 1, result.stdout)
                self.assertTrue(result.stdout.startswith("배운 것 01-02: "), result.stdout)
                rows = self.verdict_rows()
                got = {}
                for row in rows:
                    self.assertEqual((row["day"], row["parsed_by"]), ("2026-01-02", "reply"))
                    self.assertIsInstance(row["at"], str)
                    self.assertIn(row["item"], self.lines[row["n"] - 1]["ids"])
                    got.setdefault(row["n"], set()).add((row["verdict"], row.get("text")))
                self.assertEqual(got, {n: {value} for n, value in expected.items()})
                # A folded line records every item it holds.
                if 2 in expected:
                    self.assertEqual(sorted(row["item"] for row in rows if row["n"] == 2), ["i2a", "i2b"])

    def test_summary_line_names_line_numbers_per_verdict(self):
        result = self.reply("1·3·4 ok, 2 지움, 6 고침: 저녁 7시")
        self.assertEqual(result.stdout, "배운 것 01-02: 1·3·4 OK, 2 지움, 6 고침\n")

    def test_card_is_picked_by_header_else_latest_with_an_open_line(self):
        self.sent_card("2026-01-03", [["j1"], ["j2"]], sent_at="2026-01-04T11:05:00+00:00")
        self.sent_card("2026-01-04", [], sent_at="2026-01-05T11:05:00+00:00")  # quiet card: nothing to answer
        self.assertEqual(self.reply("1 ok").stdout, "배운 것 01-03: 1 OK\n")
        # 01-03 line 2 has no reply yet, so the next unaddressed reply stays on 01-03.
        self.assertEqual(self.reply("2 지움").stdout, "배운 것 01-03: 2 지움\n")
        # A quoted header picks that card even though it was already answered.
        self.assertEqual(self.reply("배운 것 01-03 · 스킬 2\n2 고쳐 다시 씀").stdout, "배운 것 01-03: 2 고침\n")
        self.assertEqual([(row["day"], row["n"], row["verdict"]) for row in self.verdict_rows()],
                         [("2026-01-03", 1, "ok"), ("2026-01-03", 2, "drop"), ("2026-01-03", 2, "fix")])
