"""Profile-scoped canonical delivery, English review and parser dispatch."""
import argparse
import json
import os
from unittest.mock import patch

from tests.hermes_cli._learned_env import LearnedSandbox


class IntegrationTests(LearnedSandbox):
    def setUp(self):
        super().setUp()
        self.sender.stop()

    def test_sender_uses_canonical_tool_in_target_profile_scope(self):
        from hermes_cli.learned.config import Config
        from hermes_cli.learned.send import attempt
        from hermes_cli.work.common import mask
        from hermes_constants import get_hermes_home
        from agent.secret_scope import current_secret_scope

        self.settings.update(send_target="telegram:synthetic", send_profile="amber")
        self.write_config()
        config = Config()
        source_home = os.environ["HERMES_HOME"]
        observed = []

        def deliver(args):
            observed.append((get_hermes_home(), current_secret_scope(), args))
            return json.dumps({"success": True})

        with patch("agent.secret_scope.build_profile_secret_scope", return_value={"TOKEN": "fixture"}), \
                patch("tools.send_message_tool.send_message_tool", side_effect=deliver):
            result = attempt(config, "Synthetic card", mask)
        self.assertIsNone(result)
        self.assertEqual(observed, [(self.profiles["amber"], {"TOKEN": "fixture"},
                                   {"action": "send", "target": "telegram:synthetic", "message": "Synthetic card"})])
        self.assertEqual(os.environ["HERMES_HOME"], source_home)

    def test_sender_masks_error_before_truncating(self):
        from hermes_cli.learned.config import Config
        from hermes_cli.learned.send import attempt
        from hermes_cli.work.common import mask
        secret = "sk-" + "SyntheticOnly" * 3
        with patch("agent.secret_scope.build_profile_secret_scope", return_value={}), \
                patch("tools.send_message_tool.send_message_tool", return_value=json.dumps({"error": "a " * 75 + secret})):
            error = attempt(Config(), "card", mask)
        self.assertNotIn(secret, error)
        self.assertIn("[가림]", error)

    def test_english_card_and_reply_follow_root_locale(self):
        from hermes_cli.config import atomic_config_replace
        from hermes_cli.config_backend import read_config_doc_readonly
        path = self.hermes / "config.yaml"
        doc = read_config_doc_readonly(path)
        doc["work"]["locale"] = "en"
        atomic_config_replace(path, doc)
        self.write_items([self.row("a", "rule", after="Keep reports brief")])
        card = self.card()
        self.assertIn("Keep reports brief", card)
        self.assertNotIn("근거", card)
        self.sent_card("2026-01-02", [["a"]])
        result = self.run_tool("learned", "reply", "Learned 01-02 1 fix: Keep reports short")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.verdict_rows()[0]["text"], "Keep reports short")

    def test_parser_builder_dispatches_package_main(self):
        from hermes_cli.subcommands.learned import build_learned_parser
        parser = argparse.ArgumentParser()
        build_learned_parser(parser.add_subparsers())
        args = parser.parse_args(["learned", "collect"])
        with patch("hermes_cli.learned.main", return_value=0) as main, self.assertRaises(SystemExit) as raised:
            args.func(args)
        self.assertEqual(raised.exception.code, 0)
        main.assert_called_once_with(["collect"])
