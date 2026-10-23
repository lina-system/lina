// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestWorkLedgerUpgradePreservesExistingCanon(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == workEngineSchema {
			index = i
		}
	}
	if index < 1 {
		t.Fatal("work migration must append to published catalog")
	}
	cfg.Migrations = all[:index]
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), ledgerFixture)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations, cfg.Epoch = all, cfg.Epoch+1
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var body string
		if err := tx.QueryRowContext(t.Context(), "SELECT body FROM conv_event WHERE id='first'").Scan(&body); err != nil {
			return err
		}
		if body != "original" {
			t.Fatalf("migration changed conversation: %q", body)
		}
		var events int
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_events").Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkLedgerMigrationAndAtomicProjection(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	const event = `INSERT INTO work_events(id,kind,at,body) VALUES('admit:goal','goal.admitted',1,'{"goals":[]}');`
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), event+`INSERT INTO work_records(kind,id,version,body) VALUES('goal','goal',1,'{}');`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE work_events SET body='{}'`,
		`DELETE FROM work_events`,
		`INSERT OR REPLACE INTO work_events SELECT * FROM work_events`,
		`INSERT INTO work_events(id,kind,at,body) VALUES('invalid','node.changed',1,'bad-json')`,
	} {
		if err := db.Write(t.Context(), func(tx *Tx) error { _, err := tx.ExecContext(t.Context(), stmt); return err }); err == nil {
			t.Fatalf("accepted invalid ledger mutation: %s", stmt)
		}
	}
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE work_records SET version=2,body='{"changed":true}' WHERE id='goal';`+event)
		return err
	}); err == nil {
		t.Fatal("duplicate event accepted")
	}
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var version int
		if err := tx.QueryRowContext(t.Context(), `SELECT version FROM work_records WHERE id='goal'`).Scan(&version); err != nil {
			return err
		}
		if version != 1 {
			t.Fatal("projection changed without its committed event")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
