from tests.hermes_cli._learned_env import LearnedSandbox


class ConclusionTests(LearnedSandbox):
    def test_error_result_and_empty_result_are_not_seen_changes(self):
        self.call("honcho_conclude", {"conclusion": "Unconfirmed"}, {})
        self.collect()
        self.assertEqual(self.items(kind="conclusion")[0]["source"], "bot_said")

    def test_create_delete_seen_and_bot_said(self):
        self.message("Keep responses brief")
        self.call("honcho_conclude", {"conclusion": "Responses should be brief", "peer": "owner-fixture"},
                  {"success": True, "id": "c1", "content": "Responses should be brief"})
        self.call("honcho_conclude", {"delete_id": "c1", "peer": "owner-fixture"}, {"success": True})
        self.call("honcho_conclude", {"conclusion": "An unverified assertion"})
        self.call("honcho_conclude", {"conclusion": "Failed assertion"}, {"success": False, "error": "no"})
        self.collect()
        rows = self.items(kind="conclusion")
        self.assertEqual(len(rows), 3)
        self.assertEqual([row["source"] for row in rows].count("seen"), 2)
        self.assertEqual([row["source"] for row in rows].count("bot_said"), 1)
        self.assertTrue(any("deleted" in row["flags"] for row in rows))
        self.assertTrue(all(row["basis"][0]["session"] == "s1" for row in rows))
        self.collect()
        self.assertEqual(self.items(kind="conclusion"), rows)

    def test_late_result_upgrades_claim_without_duplicate(self):
        import json
        import sqlite3
        from tests.hermes_cli._learned_env import epoch
        call_id = self.call("honcho_conclude", {"conclusion": "Late completion"})
        self.collect("2026-01-02T12:00:00")
        with sqlite3.connect(str(self.profiles["amber"] / "state.db")) as db:
            db.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)", (
                900, "s1", "tool", json.dumps({"success": True}), call_id, None,
                "honcho_conclude", epoch("2026-01-02T13:00:00")))
        self.collect()
        rows = self.items(kind="conclusion")
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["source"], "seen")
