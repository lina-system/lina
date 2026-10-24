// SPDX-License-Identifier: Apache-2.0
package records

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestRecordsThousands(t *testing.T) {
	t.Parallel()
	const count = 3000
	s, root, db := setup(t)
	// Seed a known published generation without timing thousands of fsynced
	// single-record creations. Subsequent edits use the production Commit path.
	started := time.Now()
	files := map[string]string{}
	for i := range count {
		id := fmt.Sprintf("record-%04d", i)
		data := []byte(fmt.Sprintf("---\nid: %q\nkind: memo\nstate: active\nperson_id: \"person\"\n---\n\n# Measurement %04d\n\nSynthetic calibration data.\n", id, i))
		name := fmt.Sprintf("inbox/measurement-%04d.md", i)
		hash, err := s.object("blob", data)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.atomic(name, data); err != nil {
			t.Fatal(err)
		}
		files[name] = hash
	}
	tree, err := s.tree(files)
	if err != nil {
		t.Fatal(err)
	}
	head, err := s.makeCommit(tree, "", actor)
	if err != nil {
		t.Fatal(err)
	}
	index, err := gitIndex(files)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.atomic(".git/index", index); err != nil {
		t.Fatal(err)
	}
	if err = s.atomic(".git/refs/heads/records", []byte(head+"\n")); err != nil {
		t.Fatal(err)
	}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE records_state SET head=?", head)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	seed := time.Since(started)
	started = time.Now()
	if err = s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	rebuild := time.Since(started)
	started = time.Now()
	r, rev, err := s.Read(t.Context(), "record-1500")
	if err != nil {
		t.Fatal(err)
	}
	read := time.Since(started)
	started = time.Now()
	rev = commit(t, s, Change{ID: r.ID, Base: rev, Path: r.Path, Data: bytes.ReplaceAll(r.Data, []byte("calibration"), []byte("revised"))})
	edit := time.Since(started)
	// Moving and an unrelated edit retain each record's own revision on rebuild.
	rev = commit(t, s, Change{ID: r.ID, Base: rev, Path: "resources/moved.md", Data: bytes.ReplaceAll(r.Data, []byte("calibration"), []byte("revised"))})
	other, otherRev, err := s.Read(t.Context(), "record-2999")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, s, Change{ID: other.ID, Base: otherRev, Path: other.Path, Data: bytes.ReplaceAll(other.Data, []byte("calibration"), []byte("final"))})
	live := indexDump(t, db)
	started = time.Now()
	if err = s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	historyRebuild := time.Since(started)
	if rebuilt := indexDump(t, db); live != rebuilt {
		t.Fatal("3000-file live and rebuilt indexes differ")
	}
	started = time.Now()
	rows, err := s.Query(t.Context(), Params{Person: "person", Search: "revised"})
	query := time.Since(started)
	if err != nil || len(rows) != 1 || rows[0].ID != r.ID || rows[0].Revision != rev {
		t.Fatal(rows, err)
	}
	// Record count and all pages are independently checked, not only one hit.
	total := 0
	cursor := ""
	for {
		rows, err := s.Query(t.Context(), Params{Person: "person", After: cursor, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		total += len(rows)
		if len(rows) < 100 {
			break
		}
		cursor = rows[len(rows)-1].ID
	}
	if total != count {
		t.Fatalf("indexed %d records, want %d", total, count)
	}
	if err = os.WriteFile(filepath.Join(root, "versions/records", other.Path), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	if _, err = s.Query(t.Context(), Params{Person: "person"}); err == nil {
		t.Fatal("outside edit accepted at scale")
	}
	detection := time.Since(started)
	t.Logf("files=%d history=4 seed=%s rebuild=%s read=%s edit=%s history_rebuild=%s query=%s outside_detection=%s", count, seed, rebuild, read, edit, historyRebuild, query, detection)
}
