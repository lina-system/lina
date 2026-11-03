import re

from tests.hermes_cli._learned_env import LearnedSandbox


class CardTextTests(LearnedSandbox):
    def test_deleted_user_facts_show_the_removed_preferences(self):
        self.memory("좋아하는 차는 홍차\n§\n좋아하는 색은 파랑", "USER.md")
        self.collect("2026-01-01T23:40:00")
        self.memory("", "USER.md")
        self.collect()
        card = self.card()
        self.assertIn("좋아하는 차는 홍차", card)
        self.assertIn("좋아하는 색은 파랑", card)
        self.assertEqual(len(self.view()["card_order"]), 2)

    def test_grouped_conclusions_include_the_count(self):
        for fact in ("따뜻한 차를 좋아함", "아침에는 조용한 시간을 원함"):
            self.call("honcho_conclude", {"conclusion": fact}, {"success": True})
        self.collect()
        self.assertEqual(len(self.view()["folded"]), 1)
        self.assertIn("새로 안 것 2개", self.card())

    def test_eight_line_korean_card_sources_and_private_counts_only(self):
        rows = [self.row("internal-id-" + str(i), "rule", target="/secret/path/" + str(i),
                         after="비밀 경로 /secret/path 및 internal-name | 값 " * 10) for i in range(8)]
        rows[0].update(source="bot_said", flags=["regressed", "no_basis"])
        rows[1].update(flags=["private", "regressed", "no_basis"], target="숨긴이름",
                       before="숨긴원문", after="숨긴원문", summary="숨긴원문", undo={"path": "숨긴이름"})
        self.write_items(rows)
        card = self.card()
        lines = card.splitlines()
        self.assertEqual(len(lines), 8)
        self.assertTrue(lines[0].startswith("배운 것 01-02"))
        self.assertTrue(lines[-1].startswith("답:"))
        for n, line in enumerate(lines[1:-1], 1):
            self.assertTrue(line.startswith(str(n) + ". "))
            self.assertRegex(line, "바뀜|봇 말")
        self.assertIn("비공개", card)
        self.assertIn("봇 말", card)
        self.assertTrue(all(len(line) <= 80 for line in lines))
        self.assertIsNone(re.search(r"[A-Za-z/|_]|숨긴원문|숨긴이름|internal", card))
        self.assertEqual(self.calls(), [])
        self.assertFalse((self.state / "sent.jsonl").exists())

    def test_korean_skill_description_first_phrase_else_generic(self):
        self.skill("", "---\nname: internal-guide\ndescription: 회의 기록을 정리한다. 뒷문장\n---\n본문", "e1")
        self.skill("", "---\ndescription: English description\n---\n본문", "e2", skill="english-guide")
        self.collect()
        card = self.card()
        self.assertIn("회의 기록을 정리한다", card)
        self.assertIn("스킬 하나", card)
        self.assertNotIn("뒷문장", card)
        self.assertNotRegex(card, r"[A-Za-z/|_]")

    def test_header_carries_totals_by_kind_in_plain_words(self):
        rows = [self.row("c" + str(n), "conclusion") for n in range(3)]
        rows += [self.row("s" + str(n), "skill", target="skill:guide" + str(n) + ":SKILL.md") for n in range(2)]
        rows += [self.row("r", "rule"), self.row("o", "soul"), self.row("p", "conclusion", flags=["private"])]
        self.write_items(rows)
        self.assertEqual(self.card().splitlines()[0], "배운 것 01-02 · 새로 안 것 4 · 스킬 2 · 규칙 2")

    def test_korean_content_naming_latin_things_is_kept(self):
        self.write_items([self.row("c1", "conclusion", target="conclusion:new", after="LINA 앱은 대화와 일을 나눠 보인다"),
                          self.row("u1", "user", after="PR 리뷰는 한 번만 받는다")])
        card = self.card()
        self.assertIn("새로 안 것 1개 · LINA 앱은 대화와 일을 나눠 보인다", card)
        self.assertIn("사용자 사실 · PR 리뷰는 한 번만 받는다", card)

    def test_content_with_paths_ids_or_no_korean_is_rejected(self):
        texts = ["~/desk/AGENTS.md 규칙을 따른다", "session 20261030_abcdef12 에서",
                 "Responses should stay short and clear", "규칙은 desk/rules 폴더의 문서를 따른다",
                 "작업 20261030abcdef12 결과를 다시 본다"]
        self.write_items([self.row("u" + str(n), "user", after=text) for n, text in enumerate(texts)])
        lines = self.card().splitlines()[1:-1]
        self.assertEqual(len(lines), len(texts))
        for line in lines:
            self.assertEqual(line.split(" · ")[2:4], ["사용자 사실", "1건 확인"], line)
            self.assertNotRegex(line, r"[A-Za-z/~]|abcdef")

    def test_mixed_sources_are_not_promoted_to_observed_changes(self):
        self.write_items([self.row("a", target="same", source="seen"),
                          self.row("b", target="same", source="bot_said")])
        card = self.card()
        self.assertIn("바뀜 1건", card)
        self.assertIn("봇 말 1건", card)
