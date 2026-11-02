from tests.hermes_cli._learned_env import LearnedSandbox


class RankTests(LearnedSandbox):
    def test_group_score_uses_combined_flags_and_background_base(self):
        self.write_items([self.row("a", "skill", target="skill:guide:SKILL.md", actor="chat", flags=["deleted"]),
                          self.row("b", "skill", target="skill:guide:notes.md", actor="background", flags=["no_basis"])])
        group, = self.view()["folded"]
        self.assertEqual(group["rank"], 8)
        self.assertEqual(group["actor"], "background")

    def test_exact_scores_flags_and_configured_bot_order(self):
        rows = [self.row(kind, kind, flags=[]) for kind in
                ("soul", "rule", "user", "memory", "conclusion", "ledger")]
        rows += [self.row("background", "skill", actor="background", flags=[]),
                 self.row("chat", "skill", actor="chat", flags=[]),
                 self.row("flags", "conclusion", flags=["regressed", "no_basis", "time_mismatch",
                                                         "self_closed", "deleted", "deleted"]),
                 self.row("tie", "soul", bot="default", flags=[])]
        self.write_items(rows)
        result = self.view()
        self.assertEqual({row["id"]: row["rank"] for row in result["items"]},
                         dict(soul=5, rule=5, user=4, memory=2, conclusion=1, ledger=1,
                              background=3, chat=2, flags=17, tie=5))
        self.assertEqual(result["folded"][0]["members"], ["conclusion", "flags"])
        self.assertEqual(result["folded"][0]["rank"], 17)
        self.assertEqual(result["folded"][1]["bot"], "default")
        self.assertEqual(result, self.view())
