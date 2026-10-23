// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestNamingMigrationPreservesExistingMemory(t *testing.T) {
	cfg := storeConfig(t)
	catalog := Migrations()
	cfg.Migrations = catalog[:len(catalog)-1]
	db := openStore(t, cfg)
	original := `{"kind":"fact","content":"synthetic"}`
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO memory_item(id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record) VALUES('before','person','all','world','','synthetic','synthetic','local','active',0,0,'',0,?)`, original)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = catalog
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var record string
		if err := tx.QueryRowContext(t.Context(), `SELECT record FROM memory_item WHERE id='before'`).Scan(&record); err != nil {
			return err
		}
		if record != original {
			t.Fatal("migration rewrote memory")
		}
		var count int
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM naming_breaches`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("migration invented breaches")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
