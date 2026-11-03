import json

from tests.hermes_cli._learned_env import LearnedSandbox


class FoldSkillTests(LearnedSandbox):
    def test_masked_long_targets_do_not_merge_different_skills(self):
        self.write_items([self.row(str(n), "skill", target="skill:" + "synthetic-guide-" * 5 + str(n) + ":SKILL.md")
                          for n in range(3)])
        self.assertEqual(len(self.view()["folded"]), 3)

    def test_one_ledger_edit_touching_two_files_is_one_edit(self):
        self.skill("예전 줄", "새 줄", "e1")
        path = self.profiles["amber"] / "skills/.curator_ledger.jsonl"
        event = json.loads(path.read_text())
        for side in ("before", "after"):
            event[side].append(dict(event[side][0], path="notes.md"))
        path.write_text(json.dumps(event) + "\n")
        self.collect()
        self.assertEqual(self.view()["folded"][0]["ops"], 1)

    def test_four_skills_collapse_plus_largest_full_net_change(self):
        common = "---\ndescription: 합성 안내를 정리한다. 다음 문장\n---\n" + "같은 줄\n" * 180
        for n in range(4):
            self.skill(common, common + "새 줄\n" * (n + 1), "e" + str(n), skill="guide" + str(n))
        self.collect()
        folded = self.view()["folded"]
        self.assertEqual(len(folded), 2)
        summary, = [row for row in folded if row["form"] == "skills"]
        detail, = [row for row in folded if row["form"] == "skill"]
        self.assertEqual((summary["count"], summary["ops"]), (4, 4))
        self.assertIn("guide3", detail["target"])
        self.assertEqual(detail["net_size"], 4)

    def test_files_in_one_skill_count_once_and_three_skills_stay_separate(self):
        self.write_items([self.row("a", "skill", target="skill:one:SKILL.md", net_size=1),
                          self.row("b", "skill", target="skill:one:notes.md", net_size=2),
                          self.row("c", "skill", target="skill:two:SKILL.md", net_size=3),
                          self.row("d", "skill", target="skill:three:SKILL.md", net_size=4)])
        rows = self.view()["folded"]
        self.assertEqual(len(rows), 3)
        self.assertEqual(sorted(len(row["members"]) for row in rows), [1, 1, 2])
