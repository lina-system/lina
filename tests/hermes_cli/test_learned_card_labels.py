from tests.hermes_cli._learned_env import LearnedSandbox

SOUL_LINE = "사용자에게는 늘 짧고 분명하게 답하고 모르는 것은 모른다고 말한다"


class CardLabelTests(LearnedSandbox):
    def shared(self, name, description):
        path = self.hermes / "shared-skills" / name / "SKILL.md"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("---\nname: " + name + "\ndescription: " + description + "\n---\n예전 본문\n")
        return path

    def change_unknown(self, paths):
        self.collect("2026-01-01T23:40:00")
        for path in paths:
            path.write_text(path.read_text() + "새 줄\n")
        self.collect()
        lines = self.card().splitlines()
        return lines[1:-1]

    def test_profile_soul_and_shared_skill_are_named_and_writer_is_unknown(self):
        soul = self.hermes / "SOUL.self.md"
        soul.write_text("# 원칙\n")
        self.list_config("rules.txt", [str(soul)])
        skill = self.shared("meeting-notes", "회의 기록을 정리한다. 뒷문장")
        self.collect("2026-01-01T23:40:00")
        soul.write_text(SOUL_LINE + "\n")
        skill.write_text(skill.read_text() + "새 줄\n")
        self.collect()
        lines = self.card().splitlines()[1:-1]
        self.assertEqual(len(lines), 2)
        soul_line, = [line for line in lines if "알파 행동 규칙(SOUL.self)" in line]
        skill_line, = [line for line in lines if "공용 스킬 회의 기록을 정리한다" in line]
        for line in lines:
            self.assertIn("누가 고쳤는지 모름", line)
            self.assertNotIn(" 봇 ", line)
            self.assertNotIn("1건 확인", line)
        self.assertNotIn("뒷문장", skill_line)
        # The content phrase is cut at a word boundary and marked with an ellipsis.
        piece = soul_line.split(" · ")[3]
        self.assertTrue(piece.endswith("…"), soul_line)
        piece = piece[:-1]
        self.assertTrue(SOUL_LINE.startswith(piece), soul_line)
        self.assertEqual(SOUL_LINE[len(piece)], " ", soul_line)
        places = {row["place"]["scope"] for row in self.view()["items"]}
        self.assertEqual(places, {"profile", "shared"})

    def test_several_unknown_shared_skill_changes_fold_into_one_line(self):
        paths = [self.shared("guide-" + str(n), "합성 안내 " + str(n) + "번을 정리한다") for n in range(3)]
        lines = self.change_unknown(paths)
        self.assertEqual(len(lines), 1, lines)
        self.assertIn("공용 스킬 3개 바뀜", lines[0])
        self.assertIn("누가 고쳤는지 모름", lines[0])
        group, = self.view()["folded"]
        self.assertEqual((group["form"], group["count"]), ("shared_skills", 3))

    def test_unplaced_rule_by_unknown_writer_is_not_called_a_bot(self):
        self.write_items([self.row("a", "rule", bot="unknown", target="/desk/rule.md", after=SOUL_LINE)])
        line = self.card().splitlines()[1]
        self.assertTrue(line.startswith("1. 누가 고쳤는지 모름 · "), line)
        piece = line.split(" · ")[3]
        self.assertTrue(piece.endswith("…"), line)
        self.assertTrue(SOUL_LINE.startswith(piece[:-1] + " "), line)
