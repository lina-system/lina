// SPDX-License-Identifier: Apache-2.0
package records

import (
	"github.com/thisisjun786/lina/internal/core/canon"
	"os"
	"path/filepath"
	"testing"
)

// Seed the publication and projections as they existed at records-search v29.
// This fixture deliberately never calls current projection/write code.
func seedLegacyRecord(t *testing.T, root string, db Canon, c Change) string {
	t.Helper()
	dir := filepath.Join(root, "versions/records")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	s := &Store{root: r, db: db}
	if err = s.initialize(); err != nil {
		t.Fatal(err)
	}
	blob, err := s.object("blob", c.Data)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{c.Path: blob}
	tree, err := s.tree(files)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := s.makeCommit(tree, "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.install(publication{Commit: rev, Path: c.Path, Blob: blob}, files); err != nil {
		t.Fatal(err)
	}
	d, err := parseDocument(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"UPDATE records_state SET head=?", []any{rev}},
			{"UPDATE records_index_state SET head=?", []any{rev}},
			{"INSERT INTO records_index VALUES(?,?,?,?)", []any{c.ID, c.Path, blob, rev}},
			{"INSERT INTO records_text VALUES(?,?,?,?,?,?)", []any{c.ID, d.person, d.kind, d.state, d.title, d.note}},
		} {
			if _, err := tx.ExecContext(t.Context(), q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rev
}
