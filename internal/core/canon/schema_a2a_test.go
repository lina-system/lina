// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestA2AUpgradePreservesUncertainWorker(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	cfg.Migrations = migrationsBefore(t, a2aSchema)
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES('kept','turn','/repo','unknown',X'7b7d')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var state string
		if err := tx.QueryRowContext(t.Context(), "SELECT state FROM worker_dispatch WHERE id='kept'").Scan(&state); err != nil {
			return err
		}
		if state != "unknown" {
			t.Fatal("upgrade changed an uncertain worker")
		}
		var count int
		if err := tx.QueryRowContext(t.Context(), "SELECT (SELECT count(*) FROM a2a_tasks)+(SELECT count(*) FROM a2a_messages)").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("upgrade invented publication work")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
