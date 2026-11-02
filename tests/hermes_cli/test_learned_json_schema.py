import json

from tests.hermes_cli._learned_env import LearnedSandbox


class JsonTests(LearnedSandbox):
    def test_masked_board_schema_private_allowlist_and_status_overlay(self):
        secret = "sk-" + "SyntheticOnly" * 3
        private = "숨겨야할개인내용"
        self.write_items([self.row("public", "rule", after="새 내용 " + secret, status="shown"),
                          self.row("private", "skill", flags=["private", "no_basis"], target=private,
                                   after=private, summary=private, description=private,
                                   basis=[{"text": private}], undo={"entries": [private]}, extra=private)])
        (self.state / "verdicts.jsonl").write_text(json.dumps(
            dict(item="public", action="fix", status="applied", at="2026-01-02T12:00:00+00:00")) + "\n")
        before = {p: p.read_bytes() for p in self.state.rglob("*") if p.is_file()}
        view = self.view()
        self.assertEqual(view["version"], 1)
        self.assertEqual(view["day"], "2026-01-02")
        self.assertNotIn(secret, json.dumps(view))
        self.assertNotIn(private, json.dumps(view, ensure_ascii=False))
        self.assertIn("[가림]", json.dumps(view, ensure_ascii=False))
        self.assertEqual(next(row for row in view["items"] if row["id"] == "public")["status"], "applied")
        order = view["card_order"]
        self.assertEqual(len(order), len(self.card().splitlines()) - 2)
        self.assertTrue(set(order).issubset({row["id"] for row in view["folded"]}))
        self.assertEqual(view["card"], self.card().rstrip("\n"))
        self.assertEqual(before, {p: p.read_bytes() for p in self.state.rglob("*") if p.is_file()})

    def test_invalid_day_rejected_without_reading_state(self):
        for value in ("../cursor", "2026-02-30", "2026-1-2"):
            result = self.run_tool("learned", "json", "--day", value)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "")
