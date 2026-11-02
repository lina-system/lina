"""Delivery freezes numbered group membership for later decisions."""
import json
from tests.hermes_cli._learned_env import LearnedSandbox

class LinesTests(LearnedSandbox):
    def test_delivered_card_freezes_all_members(self):
        self.settings["send_target"] = "telegram:fixture"
        self.write_config()
        self.write_items([self.row("r1", "rule"), self.row("m1", target="memory:file"),
                          self.row("m2", target="memory:file")])
        result = self.run_tool("learned", "card", "--day", "2026-01-02", "--send")
        self.assertEqual(result.returncode, 0, result.stderr)
        card = json.loads((self.state / "cards/2026-01-02.json").read_text())
        self.assertEqual([(line["n"], line["ids"]) for line in card["lines"]], [(1, ["r1"]), (2, ["m1", "m2"])])
        self.assertEqual(self.view()["sent_lines"], card["lines"])

    def test_failed_send_does_not_create_lines(self):
        self.settings["send_target"] = "telegram:fixture"
        self.write_config()
        self.send_result = "failed"
        self.write_items([self.row("r1", "rule")])
        self.assertEqual(self.run_tool("learned", "card", "--day", "2026-01-02", "--send").returncode, 1)
        self.assertFalse((self.state / "cards/2026-01-02.json").exists())
