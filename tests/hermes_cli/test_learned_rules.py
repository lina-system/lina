import hashlib
import json

from tests.hermes_cli._learned_env import LearnedSandbox


class RuleTests(LearnedSandbox):
    def test_long_rule_path_and_references_survive_content_masking(self):
        relative = ("profiles/amber/skills/synthetic-development/"
                    "reference-guide-for-fixture-workflows/references/example-policy-and-checks.md")
        self.assertGreaterEqual(len(relative), 90)
        rule = self.hermes / relative
        rule.parent.mkdir(parents=True)
        # Exercises the unchanged generic 48-character mask, without a key prefix.
        long_token = "SyntheticCredentialOnly" * 3  # 69 characters: hits the generic 48-character mask
        rule.write_text("Before " + long_token + "\n")
        self.list_config("rules.txt", [str(rule)])
        self.collect("2026-01-01T23:40:00")
        self.message("Update the guide using " + long_token)
        self.call("write_file", {"path": str(rule), "content": "After " + long_token + "\n"},
                  {"success": True, "content": "After " + long_token + "\n"})
        rule.write_text("After " + long_token + "\n")
        self.collect()
        item, = self.items(kind="rule")
        self.assertEqual(item["target"], str(rule))
        self.assertEqual((item["bot"], item["actor"]), ("amber", "chat"))
        self.assertEqual((item["before"], item["after"]), ("Before [가림]\n", "After [가림]\n"))
        self.assertEqual(item["basis"][0]["text"], "Update the guide using [가림]")
        diff = (self.state / item["diff"]).read_text()
        self.assertEqual(item["diff"], "blobs/" + hashlib.sha256(diff.encode()).hexdigest())
        self.assertIn("+After [가림]", diff)
        key = hashlib.sha256(str(rule).encode()).hexdigest()
        for day in ("2026-01-01", "2026-01-02"):
            snapshot = json.loads((self.state / "snap" / (day + ".json")).read_text())
            self.assertEqual(snapshot["files"][key]["target"], str(rule))
        # Deleted files must retain the exact target from the previous snapshot.
        rule.unlink()
        self.collect("2026-01-03T23:40:00")
        deleted, = self.items("2026-01-03", kind="rule")
        self.assertEqual(deleted["target"], str(rule))
        self.assertIn("deleted", deleted["flags"])
        for output in self.state.rglob("*"):
            if output.is_file():
                self.assertNotIn(long_token, output.read_text())

    def test_allowlist_shared_skills_attribution_and_unknown(self):
        soul = self.profiles["amber"] / "SOUL.md"
        rule = self.desk / "Rules/guide.md"
        shared = self.hermes / "shared-skills/sample/SKILL.md"
        outside = self.desk / "Outside.md"
        for path in (soul, rule, shared, outside):
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("Before\n")
        self.list_config("rules.txt", [str(soul), str(rule)])
        self.collect("2026-01-01T23:40:00")
        self.message("Update the guide")
        self.call("terminal", {"command": "printf new > '" + str(rule) + "'"}, {"exit_code": 0})
        for path in (soul, rule, shared, outside):
            path.write_text("After\n")
        self.collect()
        rows = self.items()
        self.assertEqual(len(rows), 3)
        self.assertEqual(next(r for r in rows if str(rule) in r["target"])["bot"], "amber")
        self.assertEqual(next(r for r in rows if r["kind"] == "soul")["actor"], "unknown")
        self.assertTrue(all(r["source"] == "seen" for r in rows))
        self.assertTrue(all("+After" in (self.state / r["diff"]).read_text() for r in rows))
