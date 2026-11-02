from tests.hermes_cli._learned_env import LearnedSandbox


class SkillTests(LearnedSandbox):
    def test_deep_skill_target_survives_content_masking(self):
        import json
        skill = "synthetic-development/reference-guide-for-fixture-workflows"
        relative = skill + "/references/example-policy-and-checks.md"
        secret = "SyntheticCredentialOnly" * 3
        self.skill("Before " + secret, "After " + secret, "e1", skill=skill)
        path = self.profiles["amber"] / "skills/.curator_ledger.jsonl"
        row = json.loads(path.read_text())
        for side in ("before", "after"):
            row[side][0]["path"] = relative
        path.write_text(json.dumps(row) + "\n")
        self.collect()
        item, = self.items(kind="skill")
        self.assertEqual(item["target"], "skill:" + skill + ":" + relative)
        self.assertEqual((item["before"], item["after"]), ("Before [가림]", "After [가림]"))
        self.assertNotIn(secret, (self.state / item["diff"]).read_text())

    def test_three_patches_one_item_and_incremental_cursor(self):
        self.message("Please simplify the guide")
        self.skill("first\n", "second\n", "e1")
        self.collect("2026-01-02T12:00:00")
        self.skill("second\n", "third\n", "e2", actor="agent")
        self.skill("third\n", "last\n", "e3")
        self.collect()
        rows = self.items(kind="skill")
        self.assertEqual(len(rows), 1)
        item = rows[0]
        self.assertEqual((item["before"], item["after"], item["ops"]), ("first\n", "last\n", 3))
        self.assertEqual(item["actors"], {"curator": 2, "agent": 1})
        self.assertEqual(item["source"], "seen")
        self.assertEqual(item["basis"][0]["text"], "Please simplify the guide")
        diff = (self.state / item["diff"]).read_text()
        self.assertIn("-first", diff)
        self.assertIn("+last", diff)
        for word in ("first\n", "second\n", "third\n", "last\n"):
            self.assertEqual((self.state / "blobs" / self.blob(word)).read_text(), word)
        self.collect()
        self.assertEqual(self.items(kind="skill"), rows)
        # Trim the source ledger and remove its blobs; retained history survives.
        (self.profiles["amber"] / "skills/.curator_ledger.jsonl").write_text("")
        for path in (self.profiles["amber"] / ".curator_backups/blobs").iterdir():
            path.unlink()
        self.collect()
        self.assertEqual(self.items(kind="skill"), rows)

    def test_trimmed_cursor_and_missing_blob(self):
        self.skill("old", "next", "e1")
        self.collect()
        (self.profiles["amber"] / "skills/.curator_ledger.jsonl").write_text("")
        self.skill("next", "newest", "e2")
        (self.profiles["amber"] / ".curator_backups/blobs" / self.blob("newest")).unlink()
        self.collect()
        row = self.items(kind="skill")[0]
        self.assertEqual(row["ops"], 2)
        self.assertIn("content_lost", row["flags"])
