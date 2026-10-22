// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestWorkerDispatchAppendsAfterMergeAuthority(t *testing.T) {
	migrations := Migrations()
	if len(migrations) < 31 || migrations[29] != (Migration{Version: 30, SQL: mergesSchema}) || migrations[30] != (Migration{Version: 31, SQL: WorkerDispatchSchema}) {
		t.Fatal("worker dispatch must append at version 31 after unchanged merge authority version 30")
	}
	cfg := storeConfig(t)
	cfg.Migrations = migrations[:30]
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO authority_merges(id,person,repository,base,state,record) VALUES('kept','owner','fixture/project','dev','queued',X'7b7d')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = migrations
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var version int
		if err := tx.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if version != len(migrations) {
			t.Fatalf("version=%d want=%d", version, len(migrations))
		}
		var state string
		if err := tx.QueryRowContext(t.Context(), "SELECT state FROM authority_merges WHERE id='kept'").Scan(&state); err != nil {
			return err
		}
		if state != "queued" {
			t.Fatal("merge authority reservation changed")
		}
		var count int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM worker_dispatch").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("upgrade invented worker dispatches")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
