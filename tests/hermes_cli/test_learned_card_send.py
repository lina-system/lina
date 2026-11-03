"""Delivery attempts are recorded, masked, and idempotent by day."""
from tests.hermes_cli._learned_env import LearnedSandbox
import json

class SendTests(LearnedSandbox):
    def setUp(self):
        super().setUp()
        self.settings["send_target"] = "telegram:fixture"
        self.write_config()

    def send(self):
        return self.run_tool("learned", "card", "--day", "2026-01-02", "--send")

    def sent(self):
        path = self.state / "sent.jsonl"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def test_card_text_is_sent_once_and_recorded(self):
        self.write_items([self.row("a", "rule"), self.row("b")])
        expected = self.card().rstrip("\n")
        result = self.send()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.send_calls, [expected])
        self.assertEqual(self.sent()[0]["items"], 2)
        self.assertTrue(self.sent()[0]["delivered"])
        self.send()
        self.assertEqual(len(self.send_calls), 1)

    def test_empty_day_records_reason_without_sending(self):
        self.assertEqual(self.send().returncode, 0)
        self.assertEqual(self.send_calls, [])
        self.assertEqual(self.sent()[0]["reason"], "empty")

    def test_failed_send_is_retried_once_without_card(self):
        self.send_result = "fixture failure"
        self.write_items([self.row("a")])
        self.assertEqual(self.send().returncode, 1)
        self.assertEqual([row["delivered"] for row in self.sent()], [False, False])
        self.assertFalse((self.state / "cards/2026-01-02.json").exists())

    def test_retry_can_deliver_after_failure(self):
        self.sender_mock.side_effect = ["failure", None]
        self.write_items([self.row("a")])
        self.assertEqual(self.send().returncode, 0)
        self.assertEqual([row["delivered"] for row in self.sent()], [False, True])

    def test_unconfigured_target_sends_nothing(self):
        self.settings["send_target"] = ""
        self.write_config()
        self.write_items([self.row("a")])
        self.assertEqual(self.send().returncode, 1)
        self.assertEqual(self.send_calls, [])
        self.assertEqual(self.sent(), [])
