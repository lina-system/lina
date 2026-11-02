from tests.hermes_cli._learned_env import LearnedSandbox

NAMES = {"lina": "하나", "sion": "둘", "kai": "셋"}


class CardConclusionTests(LearnedSandbox):
    def rows(self, counts, rules=5):
        rows = [self.row("r" + str(n), "rule", bot="unknown", after="규칙 문서 내용 " + str(n)) for n in range(rules)]
        for bot, count in counts.items():
            # File order is collection order; ids run backwards so order is not by id.
            rows += [self.row("c-" + bot + "-" + str(count - n).zfill(3), "conclusion", bot=bot,
                              target="conclusion:new", after="사용자는 " + NAMES[bot] + " 결론 " + str(n) + "번을 원함")
                     for n in range(count)]
        return rows

    def conclusion_lines(self):
        return [line for line in self.card().splitlines() if "새로 안 것" in line and line[0].isdigit()]

    def test_hundred_conclusions_reach_the_card_beside_five_rule_changes(self):
        self.write_items(self.rows({"lina": 60, "sion": 40}))
        lines = self.card().splitlines()
        self.assertLessEqual(len(lines), 8)
        found = self.conclusion_lines()
        self.assertEqual(len(found), 2, lines)
        lina, = [line for line in found if " 하나 · " in line]
        self.assertIn("새로 안 것 60개", lina)
        self.assertIn("사용자는 하나 결론 0번을 원함", lina)
        self.assertIn("새로 안 것 40개", next(line for line in found if " 둘 · " in line))
        self.assertEqual(len(self.view()["card_order"]), len(lines) - 2)

    def test_at_most_two_conclusion_lines_and_private_ones_stay_counts(self):
        rows = self.rows({"lina": 3, "sion": 2, "kai": 1})
        rows.append(self.row("p1", "conclusion", bot="rumi", flags=["private", "no_basis"], after="숨긴결론"))
        self.write_items(rows)
        card = self.card()
        self.assertEqual(len(self.conclusion_lines()), 2)
        self.assertEqual(len(card.splitlines()), 8)
        self.assertNotIn("숨긴결론", card)

    def real_day(self):
        rows = self.rows({"lina": 2, "sion": 21})
        rows += [self.row("p-lina-" + str(n), "conclusion", bot="lina", flags=["private", "no_basis"],
                          after="숨긴결론") for n in range(8)]
        for bot, count in (("lina", 6), ("kai", 4)):
            rows += [self.row("d-" + bot + "-" + str(n), "conclusion", bot=bot, target="conclusion:old-" + str(n),
                              before="사용자는 " + NAMES[bot] + " 지운 결론 " + str(n) + "번", after="",
                              flags=["deleted", "no_basis"]) for n in range(count)]
        self.write_items(rows)

    def bot_line(self, name):
        return next((line for line in self.card().splitlines() if line[0].isdigit() and " " + name + " · " in line), "")

    def test_new_deleted_and_private_counts_are_split_with_a_new_lead(self):
        self.real_day()
        line = self.bot_line("하나")
        self.assertIn("새로 안 것 2개 · 지움 6개 · 비공개 8개 · 사용자는 하나 결론 0번을 원함", line)
        self.assertNotIn("삭제", line)
        self.assertNotIn("숨긴결론", self.card())

    def test_deleted_only_lead_is_marked_and_private_only_has_no_phrase(self):
        rows = [self.row("d-kai-" + str(n), "conclusion", bot="kai", target="conclusion:old-" + str(n),
                         before="사용자는 셋 지운 결론 " + str(n) + "번", after="", flags=["deleted", "no_basis"])
                for n in range(4)]
        rows += [self.row("p-rumi-" + str(n), "conclusion", bot="rumi", flags=["private", "no_basis"],
                          after="숨긴결론") for n in range(3)]
        self.write_items(rows)
        kai = self.bot_line("셋")
        self.assertIn("지움 4개 · 사용자는 셋 지운 결론 0번(지움)", kai)
        self.assertNotIn("새로 안 것", kai)
        self.assertNotIn("삭제", kai)
        rumi = self.bot_line("넷")
        self.assertEqual(rumi.split(" · ")[2:4], ["비공개 3개", "근거 없음"], rumi)

    def test_reserved_lines_go_to_the_most_new_public_conclusions(self):
        self.real_day()
        self.assertIn("새로 안 것 21개", self.bot_line("둘"))
        self.assertIn("새로 안 것 2개", self.bot_line("하나"))
        self.assertEqual(self.bot_line("셋"), "")

    def test_reserved_ties_fall_back_to_deleted_then_bot_order(self):
        rows = self.rows({"lina": 1, "sion": 1, "kai": 1})
        rows.append(self.row("d-kai", "conclusion", bot="kai", target="conclusion:old", before="사용자는 지움",
                             after="", flags=["deleted", "no_basis"]))
        self.write_items(rows)
        self.assertNotEqual(self.bot_line("셋"), "")
        # Configured order (default, amber) names neither; the unknown bots then sort by name.
        self.assertNotEqual(self.bot_line("하나"), "")
        self.assertEqual(self.bot_line("둘"), "")

    def test_lead_skips_conclusions_whose_phrase_is_rejected(self):
        self.write_items([
            self.row("c1", "conclusion", bot="lina", target="conclusion:new", after="~/desk/AGENTS.md 규칙을 따른다"),
            self.row("c2", "conclusion", bot="lina", target="conclusion:new", after="사용자는 짧은 보고를 원한다"),
            self.row("d1", "conclusion", bot="kai", target="conclusion:a", before="session_id 기록을 지웠다",
                     after="", flags=["deleted", "no_basis"]),
            self.row("d2", "conclusion", bot="kai", target="conclusion:b", before="사용자는 밤 알림을 싫어한다",
                     after="", flags=["deleted", "no_basis"])])
        self.assertIn("새로 안 것 2개 · 사용자는 짧은 보고를 원한다", self.bot_line("하나"))
        self.assertIn("지움 2개 · 사용자는 밤 알림을 싫어한다(지움)", self.bot_line("셋"))

    def test_deleted_conclusions_fold_with_new_ones_per_bot(self):
        rows = self.rows({"lina": 2}, rules=0)
        rows.append(self.row("d1", "conclusion", bot="lina", target="conclusion:old-id",
                             before="사용자는 지운 결론", after="", flags=["deleted", "no_basis"]))
        self.write_items(rows)
        found = self.conclusion_lines()
        self.assertEqual(len(found), 1, found)
        self.assertIn("새로 안 것 2개 · 지움 1개", found[0])
