// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"testing"
)

func TestTeamsMigrationPreservesMessengerSources(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	cfg.Migrations = migrationsBefore(t, TeamsSchema)
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO messenger_source(person,platform,account,mode,adapter,state,connected_at) VALUES('owner','fixture','account','official','fixture','connected','2026-10-24T00:00:00Z'); INSERT INTO messenger_chat(platform,account,chat,kind,cursor) VALUES('fixture','account','chat','direct','kept')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	cfg.Epoch++
	db = openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO teams_connection VALUES('owner','work','{}'); INSERT INTO messenger_author VALUES('teams','tenant:owner','tenant:sender','Sender','sender@example.test')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var cursor, email string
		if err := tx.QueryRowContext(t.Context(), "SELECT cursor FROM messenger_chat WHERE chat='chat'").Scan(&cursor); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT email FROM messenger_author WHERE author='tenant:sender'").Scan(&email); err != nil {
			return err
		}
		if cursor != "kept" || email != "sender@example.test" {
			t.Fatal("migration lost source/profile data")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
