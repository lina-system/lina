from tests.hermes_cli._learned_env import LearnedSandbox


class PrivacyTests(LearnedSandbox):
    def test_private_individual_file_with_skill_relative_ledger_paths(self):
        import json
        hidden = "Synthetic confidential individual fern note"
        self.list_config("private.txt", [str(self.profiles["amber"] / "skills/sample/notes/private.md")])
        self.skill(hidden, hidden + " changed", "e1")
        path = self.profiles["amber"] / "skills/.curator_ledger.jsonl"
        row = json.loads(path.read_text())
        for side in ("before", "after"):
            row[side][0]["path"] = "notes/private.md"
        path.write_text(json.dumps(row) + "\n")
        self.collect()
        item = self.items(kind="skill")[0]
        self.assertIn("private", item["flags"])
        self.assertEqual((item["before"], item["after"], item["target"]), ("", "", "private"))
        for output in self.state.rglob("*"):
            if output.is_file():
                self.assertNotIn(hidden, output.read_text())
                self.assertNotIn("notes/private.md", output.read_text())

    def test_private_skill_directory_with_file_relative_ledger_paths(self):
        import json
        hidden = "Synthetic private fern instructions"
        self.list_config("private.txt", [str(self.profiles["amber"] / "skills/sealed-guide")])
        self.skill(hidden, hidden + " changed", "e1", skill="sealed-guide")
        path = self.profiles["amber"] / "skills/.curator_ledger.jsonl"
        row = json.loads(path.read_text())
        row["before"][0]["path"] = "SKILL.md"
        row["after"][0]["path"] = "SKILL.md"
        path.write_text(json.dumps(row) + "\n")
        self.collect()
        self.assertIn("private", self.items(kind="skill")[0]["flags"])
        self.assertFalse(any(hidden in p.read_text() for p in self.state.rglob("*") if p.is_file()))

    def test_private_counts_only_and_secrets_masked_everywhere(self):
        private = "Synthetic confidential orchid detail"
        secret = "sk-" + "SyntheticOnly" * 3
        rule = self.desk / "hidden-garden/rule.md"
        rule.parent.mkdir(parents=True)
        rule.write_text(private)
        self.list_config("rules.txt", [str(rule)])
        self.list_config("private.txt", [str(rule.parent), "skill:amber:sealed-guide", "conclusion:default", "memory:amber:USER.md"])
        self.memory(private, "USER.md")
        self.memory("A harmless baseline")
        self.collect("2026-01-01T23:40:00")
        self.skill(private, private + " changed", "e1", skill="sealed-guide")
        self.message(private)
        self.call("honcho_conclude", {"conclusion": private, "peer": "another-fixture"}, {"success": True})
        self.call("honcho_conclude", {"conclusion": private}, {"success": True}, profile="default")
        self.memory("API access " + secret)
        self.memory(private + " changed", "USER.md")
        rule.write_text(private + " changed")
        result = self.collect()
        rows = self.items()
        private_rows = [r for r in rows if "private" in r["flags"]]
        self.assertEqual(len(private_rows), 5)
        for row in private_rows:
            self.assertEqual((row["before"], row["after"], row["basis"], row["diff"]), ("", "", [], ""))
            self.assertEqual(row["target"], "private")
            self.assertGreater(row["ops"], 0)
        self.assertTrue(any("[가림]" in r["after"] for r in rows))
        for path in self.state.rglob("*"):
            if path.is_file():
                data = path.read_text()
                self.assertNotIn(private, data, str(path.relative_to(self.state)))
                self.assertNotIn(secret, data)
                self.assertNotIn("sealed-guide", data)
                self.assertNotIn("hidden-garden", data)
        self.assertNotIn(private, result.stdout + result.stderr)
        self.assertEqual(self.state.stat().st_mode & 0o777, 0o700)
