// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestSteeringMigrationPreservesExistingLedger(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = migrationsBefore(t, steeringSchema)
	db := openStore(t, cfg)
	const record = `{"kind":"created","task_id":"legacy"}`
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_ledger(task_id,event_key,record) VALUES('legacy','legacy',?)", record)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	cfg.Epoch++
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var stored string
		if err := tx.QueryRowContext(t.Context(), "SELECT record FROM work_ledger WHERE event_key='legacy'").Scan(&stored); err != nil {
			return err
		}
		if stored != record {
			t.Fatal("migration changed prior facts")
		}
		var n int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_steering").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("migration invented steering")
		}
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_steering_receipts").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
}
