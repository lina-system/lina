// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestRecordOperationsMigrationPreservesPublishedRecords(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, m := range all {
		if m.SQL == recordOperationsSchema {
			index = i
		}
	}
	if index < 1 {
		t.Fatal("record operations migration missing")
	}
	cfg.Migrations = all[:index]
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE records_state SET head='published-head'; INSERT INTO records_index(id,path,blob,revision) VALUES('memo','inbox/note.md','published-blob','published-head')")
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
		var head, path, blob, revision string
		if err := tx.QueryRowContext(t.Context(), "SELECT s.head,i.path,i.blob,i.revision FROM records_state s JOIN records_index i ON i.id='memo'").Scan(&head, &path, &blob, &revision); err != nil {
			return err
		}
		if head != "published-head" || path != "inbox/note.md" || blob != "published-blob" || revision != head {
			t.Fatal("migration changed record identity/history")
		}
		for _, table := range []string{"record_operations", "record_candidates"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatal("migration invented operation", table, n)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
