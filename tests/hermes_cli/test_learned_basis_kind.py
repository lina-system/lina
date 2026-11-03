from tests.hermes_cli._learned_env import LearnedSandbox


class BasisTests(LearnedSandbox):
    def test_prefixes_session_source_and_previous_two_messages(self):
        import sqlite3
        cases = [("plain request", "telegram", "owner"), ("Message from 🤖 helper", "telegram", "bot"),
                 ("[장부 검사기] check", "telegram", "checker"), ("[Grok 수집] report", "telegram", "bot"),
                 ("scheduled instructions", "cron", "cron")]
        for i, (content, source, kind) in enumerate(cases):
            session = "session-" + str(i)
            with sqlite3.connect(str(self.profiles["amber"] / "state.db")) as db:
                db.execute("INSERT INTO sessions VALUES (?,?,?,?)", (session, source, "Fixture", 0))
            self.message("Earlier context", session=session, at="2026-01-02T09:00:00")
            self.message(content, session=session)
            self.call("honcho_conclude", {"conclusion": "Statement " + str(i)}, {"success": True}, session=session)
        self.collect()
        rows = self.items(kind="conclusion")
        self.assertEqual(len(rows), len(cases))
        for row in rows:
            i = int(row["after"].split()[-1])
            self.assertEqual(row["basis"][0]["kind"], cases[i][2])
            self.assertEqual(row["basis"][1]["text"], "Earlier context")
