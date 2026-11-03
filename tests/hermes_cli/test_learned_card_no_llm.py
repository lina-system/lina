"""LLM rewriting is optional and receives masked public summaries only."""
from unittest.mock import patch
from tests.hermes_cli._learned_env import LearnedSandbox

class NoLlmTests(LearnedSandbox):
    def test_unconfigured_llm_is_identical_without_a_call(self):
        self.write_items([self.row("a", "rule")])
        with patch("hermes_cli.learned.card.rewrite") as rewrite:
            self.assertEqual(self.card(), self.card(llm=True))
        rewrite.assert_not_called()

    def test_llm_is_opt_in_and_private_groups_never_cross_boundary(self):
        self.settings["llm_command"] = "synthetic-model"
        self.write_config()
        secret = "sk-" + "SyntheticOnly" * 3
        self.write_items([self.row("a", "rule", after=secret),
                          self.row("b", "skill", flags=["private", "no_basis"], after="hidden")])
        with patch("hermes_cli.learned.card.rewrite", side_effect=lambda cmd, texts: texts) as rewrite:
            self.card()
            rewrite.assert_not_called()
            self.card(llm=True)
        self.assertEqual(rewrite.call_count, 1)
        sent = rewrite.call_args.args[1]
        self.assertEqual(len(sent), 1)
        self.assertNotIn(secret, str(sent))
        self.assertNotIn("hidden", str(sent))
