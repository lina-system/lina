// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestWorkerRecoveryUpgradePreservesPublishedQuestions(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	// Published dev installs questions at 51, before worker recovery exists.
	cfg.Migrations = append(append([]Migration{}, Migrations()[:50]...), Migration{Version: 51, SQL: conversationQuestionsSchema})
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO conversation_questions(id,person,agent,turn,record) VALUES('q','person','agent','turn','{"question":"retained"}');
INSERT INTO conversation_question_resolutions(question,record) VALUES('q','{"answer":"retained"}');`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO worker_recovery(id,record) VALUES('worker','{"state":"unknown"}')`)
		return err
	}); err != nil {
		t.Fatalf("upgraded published questions database cannot retain recovery: %v", err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		for _, row := range []struct{ query, want string }{
			{`SELECT record FROM conversation_questions WHERE id='q'`, `{"question":"retained"}`},
			{`SELECT record FROM conversation_question_resolutions WHERE question='q'`, `{"answer":"retained"}`},
			{`SELECT record FROM worker_recovery WHERE id='worker'`, `{"state":"unknown"}`},
		} {
			var got string
			if err := tx.QueryRowContext(t.Context(), row.query).Scan(&got); err != nil {
				return err
			}
			if got != row.want {
				t.Errorf("upgrade or reopen changed retained record: got=%s want=%s", got, row.want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
