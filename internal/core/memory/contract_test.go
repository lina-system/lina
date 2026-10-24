// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"fmt"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestRepeatedStatementStorageGrowsLinearly(t *testing.T) {
	db := database(t)
	var previous int64
	for i := 1; i <= 200; i++ {
		in := proposal("A", "same statement")
		in.Item.Evidence[0].ID = fmt.Sprintf("A/event-%03d", i)
		item := commit(t, db, in)
		if len(item.Evidence) != i || item.Revision != int64(i) {
			t.Fatalf("merge %d: revision=%d evidence=%d", i, item.Revision, len(item.Evidence))
		}
		if i != 50 && i != 100 && i != 200 {
			continue
		}
		if err := db.Read(t.Context(), func(tx *canon.Tx) error {
			var bytes, revisions, citations, embedded int64
			if err := tx.QueryRowContext(t.Context(), `SELECT
 (SELECT sum(length(record)) FROM memory_item)+(SELECT sum(length(record)) FROM memory_revision)+
 (SELECT sum(length(view_id)+length(added_revision)+length(target_id)+length(target_revision)+length(source_agent)+32) FROM cite),
 (SELECT count(*) FROM memory_revision),(SELECT count(*) FROM cite),
 (SELECT count(*) FROM memory_revision WHERE json_type(record,'$.evidence') IS NOT NULL)`).Scan(&bytes, &revisions, &citations, &embedded); err != nil {
				return err
			}
			t.Logf("merges=%d revisions=%d citations=%d stored_bytes=%d embedded_revisions=%d", i, revisions, citations, bytes, embedded)
			if revisions != int64(i) || citations != int64(i) || embedded != 0 {
				t.Errorf("noncanonical storage: revisions=%d citations=%d embedded=%d", revisions, citations, embedded)
			}
			if previous > 0 && bytes > previous*22/10 {
				t.Errorf("doubling merges grew storage from %d to %d bytes", previous, bytes)
			}
			previous = bytes
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
