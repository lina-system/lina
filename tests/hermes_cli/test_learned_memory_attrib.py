from tests.hermes_cli._learned_env import LearnedSandbox


class MemoryTests(LearnedSandbox):
    def test_unrelated_replacement_attributes_added_and_removed_entries(self):
        self.memory("Old violet instructions")
        self.collect("2026-01-01T23:40:00")
        self.call("memory", {"action": "replace", "target": "memory", "content": "Fresh orange guidance"},
                  {"replaced_entry": "Old violet instructions"})
        self.memory("Fresh orange guidance")
        self.collect()
        rows = self.items(kind="memory")
        self.assertEqual(len(rows), 2)
        self.assertEqual({r["actor"] for r in rows}, {"chat"})

    def test_daily_net_entries_results_and_unknown_actor(self):
        self.memory("The guide uses blue ink\n§\nRemove this entry\n§\nPatch old entry")
        self.collect("2026-01-01T23:40:00")
        self.assertEqual(self.items("2026-01-01"), [])
        self.message("Use green ink instead")
        self.call("memory", {"action": "replace", "target": "memory", "content": "The guide uses green ink"},
                  {"replaced_entry": "The guide uses blue ink"})
        self.call("memory", {"action": "remove", "target": "memory"}, {"removed_entry": "Remove this entry"})
        self.call("memory", {"action": "patch", "target": "memory"},
                  {"patched_entry_before": "Patch old entry", "patched_entry_after": "Patch new entry"})
        self.memory("The guide uses green ink\n§\nPatch new entry\n§\nA new unrecorded preference")
        self.collect("2026-01-02T12:00:00")
        self.assertEqual(self.items(kind="memory"), [])
        self.collect()
        rows = self.items(kind="memory")
        self.assertEqual(len(rows), 4)
        self.assertEqual(sum(row["actor"] == "chat" for row in rows), 3)
        self.assertEqual(sum(row["actor"] == "unknown" for row in rows), 1)
        self.assertTrue(all(row["source"] == "seen" for row in rows))
        self.assertTrue(any(row["before"] == "Remove this entry" and "deleted" in row["flags"] for row in rows))
        self.assertFalse(any("wrong entry" in str(row) for row in rows))
        self.collect()
        self.assertEqual(self.items(kind="memory"), rows)

    def test_user_file_lock_ignored_and_multi_call_chain(self):
        self.memory("A moderately long original preference", "USER.md")
        self.collect("2026-01-01T23:40:00")
        self.call("memory", {"action": "replace", "target": "user", "content": "A moderately long middle preference"},
                  {"replaced_entry": "A moderately long original preference"})
        self.call("memory", {"action": "replace", "target": "user", "content": "A moderately long final preference"},
                  {"replaced_entry": "A moderately long middle preference"})
        self.memory("A moderately long final preference", "USER.md")
        self.memory("Ignore this", "USER.md.lock")
        self.collect()
        rows = self.items(kind="user")
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["ops"], 2)
        self.assertEqual(rows[0]["actor"], "chat")
