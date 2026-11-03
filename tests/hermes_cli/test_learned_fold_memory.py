from tests.hermes_cli._learned_env import LearnedSandbox


class FoldMemoryTests(LearnedSandbox):
    def test_user_facts_stay_individually_addressable(self):
        self.memory("좋아하는 차는 홍차\n§\n좋아하는 색은 파랑", "USER.md")
        self.collect("2026-01-01T23:40:00")
        self.memory("좋아하는 차는 녹차\n§\n좋아하는 색은 초록", "USER.md")
        self.collect()
        view = self.view()
        self.assertEqual(len(view["items"]), 2)
        self.assertEqual(len(view["folded"]), 2)
        self.assertTrue(all(len(row["members"]) == 1 for row in view["folded"]))
        self.assertEqual(len(view["card_order"]), 2)
        self.assertIn("좋아하는 차는 녹차", self.card())
        self.assertIn("좋아하는 색은 초록", self.card())

    def test_deletion_only_has_distinct_addressable_summary_and_detail(self):
        self.write_items([self.row("deleted", flags=["deleted", "no_basis"], change="removed", after="")])
        view = self.view()
        self.assertEqual(len(view["folded"]), 2)
        self.assertEqual(len(set(view["card_order"])), 2)

    def test_183_calls_fold_to_summary_deletion_and_user_change(self):
        self.memory("기억 문장을 고쳐 쓰기 0\n§\n사라질 항목")
        self.memory("사용자에 대한 예전 사실", "USER.md")
        self.call("memory", {"target": "memory", "action": "add", "content": "기준"},
                  {"success": True, "usage_percent": 20}, at="2026-01-01T11:00:00")
        self.collect("2026-01-01T23:40:00")
        for n in range(181):
            self.call("memory", {"action": "replace", "target": "memory",
                                 "content": "기억 문장을 고쳐 쓰기 " + str(n + 1)},
                      {"replaced_entry": "기억 문장을 고쳐 쓰기 " + str(n), "usage_percent": 75})
        self.call("memory", {"action": "remove", "target": "memory"},
                  {"removed_entry": "사라질 항목", "usage_percent": 75})
        self.call("memory", {"action": "replace", "target": "user", "content": "사용자에 대한 새 사실"},
                  {"replaced_entry": "사용자에 대한 예전 사실"})
        self.memory("기억 문장을 고쳐 쓰기 181")
        self.memory("사용자에 대한 새 사실", "USER.md")
        self.collect()
        view = self.view()
        self.assertEqual(len(view["folded"]), 3)
        summary, = [row for row in view["folded"] if row.get("form") == "memory"]
        self.assertEqual(summary["ops"], 182)
        self.assertEqual(summary["net"], {"added": 0, "changed": 1, "removed": 1})
        self.assertEqual(summary["usage"], {"before": 20, "after": 75})
        card = self.card()
        self.assertIn("182번", card)
        self.assertIn("20%→75%", card)
        self.assertIn("삭제 1", card)

    def test_fold_is_explicit_atomic_and_does_not_change_raw_items(self):
        self.write_items([self.row("a", target="memory:file", change="added"),
                          self.row("b", target="memory:file", change="changed")])
        path = self.state / "items/2026-01-02.jsonl"
        before = path.read_bytes()
        first = self.run_tool("learned", "fold", "--day", "2026-01-02")
        self.assertEqual(first.returncode, 0, first.stderr)
        output = self.state / "items/2026-01-02.folded.json"
        data = output.read_bytes()
        self.assertEqual(self.run_tool("learned", "fold", "--day", "2026-01-02").returncode, 0)
        self.assertEqual(output.read_bytes(), data)
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(output.stat().st_mode & 0o777, 0o600)
        self.assertEqual(len(self.view()["folded"]), 1)
