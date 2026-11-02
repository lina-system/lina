from tests.hermes_cli._learned_env import LearnedSandbox, epoch


class LedgerTests(LearnedSandbox):
    def test_terminal_read_is_not_a_self_written_file(self):
        path = self.desk / "Notes/result.md"
        self.call("terminal", {"command": "cat '" + str(path) + "'"}, {"exit_code": 0})
        self.ledger_rows(dict(id="G1", op="done", ts=epoch("2026-01-02T12:00:00"), by="amber",
                             check="evidence:A meeting happened", evidence=[{"ref": "desk:Notes/result.md"}]))
        self.collect()
        self.assertNotIn("self_closed", self.items(kind="ledger")[0]["flags"])

    def test_basis_found_missing_mismatch_and_self_closed(self):
        self.message("Build the sample, please!", at="2026-01-02T01:02:00", profile="default")
        self.call("write_file", {"path": str(self.desk / "Notes/result.md"), "content": "Synthetic memo"},
                  {"success": True}, at="2026-01-02T01:04:00")
        def row(id_, text, at, **extra):
            return dict(id=id_, op="basis", ts=epoch("2026-01-02T01:05:00"), by="amber", writer="amber",
                        basis=[dict(text=text, at=epoch(at), src="01:0x synthetic")], **extra)
        self.ledger_rows(row("G1", "Build the sample please", "2026-01-02T01:02:00"),
                         row("G2", "Build the sample please", "2026-01-02T12:00:00"),
                         row("G3", "An absent request", "2026-01-02T01:02:00"))
        self.ledger_rows(dict(id="G4", op="done", ts=epoch("2026-01-02T01:06:00"), by="amber", writer="checker",
                              check="evidence:A meeting happened", evidence=[{"ref": "desk:Notes/result.md", "by": "amber"}]))
        self.collect()
        rows = {row["target"]: row for row in self.items(kind="ledger")}
        self.assertEqual(rows["ledger:G1"]["basis"][0]["verification"], "found")
        self.assertEqual(rows["ledger:G1"]["basis"][0]["profile"], "default")
        self.assertEqual(rows["ledger:G2"]["basis"][0]["verification"], "time_mismatch")
        self.assertEqual(rows["ledger:G3"]["basis"][0]["verification"], "not_found")
        self.assertIn("self_closed", rows["ledger:G4"]["flags"])
        before = self.items()
        self.collect()
        self.assertEqual(self.items(), before)

    def test_work_store_events_are_incremental(self):
        self.ledger_rows(dict(id="T1", op="add", ts=epoch("2026-01-02T11:00:00"), title="First"))
        self.collect()
        first = self.items(kind="ledger")
        self.collect()
        self.assertEqual(self.items(kind="ledger"), first)
        self.ledger_rows(dict(id="T1", op="edit", ts=epoch("2026-01-02T12:00:00"), title="Changed"))
        self.collect()
        self.assertEqual(len(self.items(kind="ledger")), 2)
