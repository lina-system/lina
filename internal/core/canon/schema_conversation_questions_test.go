// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestConversationQuestionsMigration(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	for i, migration := range all {
		if migration.SQL == conversationQuestionsSchema {
			cfg.Migrations = all[:i]
			break
		}
	}
	old, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = old.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "CREATE TABLE question_upgrade_fixture(value TEXT); INSERT INTO question_upgrade_fixture VALUES('preserved')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = all
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(t.Context())
	err = s.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO conversation_questions(id,person,agent,turn,record) VALUES('q','p','a','t','{}')`)
		return err
	})
	if err != nil {
		t.Fatalf("pending question cannot be persisted: %v", err)
	}
	if err = s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(t.Context())
	if err = reopened.Read(t.Context(), func(tx *Tx) error {
		var kept string
		if err := tx.QueryRowContext(t.Context(), "SELECT value FROM question_upgrade_fixture").Scan(&kept); err != nil {
			return err
		}
		if kept != "preserved" {
			t.Errorf("previous data changed: %s", kept)
		}
		var count int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conversation_questions").Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("pending question lost: %d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
