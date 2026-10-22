// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestWorkCompletionMigrationPreservesLedger(t *testing.T) {
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == WorkCompletionsSchema {
			index = i
			break
		}
	}
	if index < 1 {
		t.Fatal("completion migration missing")
	}
	cfg.Migrations = all[:index]
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
	cfg.Migrations = all
	cfg.Epoch++
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var stored string
		if err := tx.QueryRowContext(t.Context(), "SELECT record FROM work_ledger WHERE event_key='legacy'").Scan(&stored); err != nil {
			return err
		}
		if stored != record {
			t.Fatal("migration changed prior ledger bytes")
		}
		var n int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_completions").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("migration invented a completion")
		}
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_completion_claims").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
}
