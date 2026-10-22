// SPDX-License-Identifier: Apache-2.0
package records

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestIndexRebuiltOnOpen(t *testing.T) {
	s, root, db := setup(t)
	c := Change{ID: "stable", Path: "inbox/note.md", Data: doc("stable", "Prototype")}
	rev := commit(t, s, c)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM records_index")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var got string
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT revision FROM records_index WHERE id='stable'").Scan(&got)
	})
	if err != nil || got != rev {
		t.Fatalf("startup did not rebuild file index: revision=%q error=%v", got, err)
	}
}

func TestIndexSearchProjectionLiveAndRebuilt(t *testing.T) {
	s, _, db := setup(t)
	commit(t, s, Change{ID: "stable", Path: "inbox/note.md", Data: []byte("---\nid: \"stable\"\nkind: memo\nstate: active\nperson_id: \"person\"\nbelongs_to: [\"goal\"]\nblocked_by: [\"dependency\"]\n---\n\n# Prototype\n\nCalibration measurements.\n")})
	check := func() {
		t.Helper()
		err := db.Read(t.Context(), func(tx *canon.Tx) error {
			var hits, edges int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM records_fts WHERE records_fts MATCH 'Calibration'").Scan(&hits); err != nil {
				return err
			}
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM records_edges WHERE source='stable'").Scan(&edges); err != nil {
				return err
			}
			if hits != 1 || edges != 2 {
				t.Errorf("search projection hits=%d edges=%d", hits, edges)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check()
	if err := s.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	check()
}

// Compare every projected value, including FTS and edges, without rowid noise.
func indexDump(t *testing.T, db Canon) string {
	t.Helper()
	var dump [][][]string
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, query := range []string{
			"SELECT id,path,blob,revision FROM records_index ORDER BY id",
			"SELECT id,person,kind,state,title,note FROM records_text ORDER BY id",
			"SELECT id,title,note FROM records_fts ORDER BY id",
			"SELECT id,title,note FROM records_korean_fts ORDER BY id",
			"SELECT gram,id FROM records_korean_bigram ORDER BY gram,id",
			"SELECT source,relation,target FROM records_edges ORDER BY source,relation,target",
			"SELECT head FROM records_index_state",
			"SELECT id,fields,when_value,deadline,repeat_next FROM records_todo ORDER BY id",
		} {
			rs, err := tx.QueryContext(t.Context(), query)
			if err != nil {
				return err
			}
			cols, err := rs.Columns()
			if err != nil {
				rs.Close()
				return err
			}
			table := [][]string{}
			for rs.Next() {
				row := make([]string, len(cols))
				args := make([]any, len(cols))
				for i := range row {
					args[i] = &row[i]
				}
				if err = rs.Scan(args...); err != nil {
					rs.Close()
					return err
				}
				table = append(table, row)
			}
			err = rs.Err()
			rs.Close()
			if err != nil {
				return err
			}
			dump = append(dump, table)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(dump)
	return string(raw)
}

func TestIndexQueryMoveUpdateAndRebuildEquality(t *testing.T) {
	s, _, db := setup(t)
	data := []byte("---\nid: \"first\"\nkind: memo\nstate: active\nperson_id: \"person\"\nbelongs_to: [\"goal\"]\nblocked_by: [\"dependency\"]\n---\n\n# Prototype\n\nCalibration measurements.\n")
	c := Change{ID: "first", Path: "inbox/note.md", Data: data}
	c.Base = commit(t, s, c)
	commit(t, s, Change{ID: "other", Path: "inbox/other.md", Data: bytes.ReplaceAll(data, []byte("first"), []byte("other"))})
	foreign := bytes.ReplaceAll(data, []byte("first"), []byte("foreign"))
	foreign = bytes.ReplaceAll(foreign, []byte(`person_id: "person"`), []byte(`person_id: "elsewhere"`))
	commit(t, s, Change{ID: "foreign", Path: "inbox/foreign.md", Data: foreign})
	rows, err := s.Query(t.Context(), Params{Person: "person", Search: "Calibration", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].ID != "first" || rows[0].Revision != c.Base {
		t.Fatal(rows, err)
	}
	rows, err = s.Query(t.Context(), Params{Person: "person", After: rows[0].ID})
	if err != nil || len(rows) != 1 || rows[0].ID != "other" {
		t.Fatal(rows, err)
	}
	c.Path = "resources/moved.md"
	c.Data = bytes.ReplaceAll(c.Data, []byte("Calibration"), []byte("Updated"))
	c.Data = bytes.ReplaceAll(c.Data, []byte(`["dependency"]`), []byte(`[]`))
	c.Base = commit(t, s, c)
	before := indexDump(t, db)
	if err = s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after := indexDump(t, db); before != after {
		t.Fatal("full live/rebuilt projections differ", before, after)
	}
	rows, err = s.Query(t.Context(), Params{Person: "person", Search: "Updated"})
	if err != nil || len(rows) != 1 || rows[0].Revision != c.Base {
		t.Fatal(rows, err)
	}
	rows, err = s.Query(t.Context(), Params{Person: "person", Search: `" OR *`})
	if err != nil || len(rows) != 0 {
		t.Fatal("FTS input interpreted as operators", rows, err)
	}
	// A stale derived head is repaired on the next operation, not only on startup.
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE records_index_state SET head='stale'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Query(t.Context(), Params{Person: "person"}); err != nil {
		t.Fatal(err)
	}
	if after := indexDump(t, db); before != after {
		t.Fatal("stale index not repaired")
	}
}

func TestOutsideChangesAreValidatedShownAndNeverAdopted(t *testing.T) {
	for _, kind := range []string{"modified", "invalid", "added", "duplicate_id", "deleted", "moved", "symlink", "committed", "locked"} {
		t.Run(kind, func(t *testing.T) {
			s, root, db := setup(t)
			c := Change{ID: "stable", Path: "inbox/note.md", Data: doc("stable", "Original")}
			c.Base = commit(t, s, c)
			before := indexDump(t, db)
			repo := filepath.Join(root, "versions/records")
			target := filepath.Join(repo, c.Path)
			write := func(name string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			want, validation := kind, "valid"
			switch kind {
			case "modified":
				write(c.Path, doc("stable", "Outside"))
			case "invalid":
				write(c.Path, []byte("malformed outside data"))
				want = "modified"
				validation = "invalid"
			case "added":
				write("inbox/new.md", doc("new", "Outside"))
			case "duplicate_id":
				write("inbox/duplicate.md", doc("stable", "Outside"))
				want = "added"
				validation = "duplicate_id"
			case "deleted":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				validation = "missing"
			case "moved":
				if err := os.Rename(target, filepath.Join(repo, "inbox/renamed.md")); err != nil {
					t.Fatal(err)
				}
				want = "deleted"
				validation = "missing"
			case "symlink":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "unreadable"), target); err != nil {
					t.Fatal(err)
				}
				want = "modified"
				validation = "invalid"
			case "committed":
				write(c.Path, doc("stable", "Outside"))
				git(t, root, "add", ".")
				git(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Outside")
				want = "repository"
				validation = "unaccepted"
			case "locked":
				write(".git/index.lock", []byte("foreign lock"))
				want = "repository"
				validation = "locked"
			}
			refBefore := git(t, root, "rev-parse", "HEAD")
			_, _, err := s.Read(t.Context(), c.ID)
			var outside *OutsideError
			if !errors.As(err, &outside) {
				t.Fatalf("missing outside notice: %v", err)
			}
			found := false
			for _, change := range outside.Changes {
				if change.Kind == want && change.Validation == validation {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s/%s: %+v", want, validation, outside)
			}
			wire, _ := json.Marshal(outside)
			if bytes.Contains(wire, []byte("inbox/")) || bytes.Contains(wire, []byte("Outside")) {
				t.Fatal("outside bytes or paths exposed", string(wire))
			}
			if err = s.Rebuild(t.Context()); !errors.Is(err, ErrRecordsChanged) {
				t.Fatal("rebuild adopted outside change", err)
			}
			if _, err = s.Commit(t.Context(), c, actor); !errors.Is(err, ErrRecordsChanged) {
				t.Fatal("write overwrote outside change", err)
			}
			if _, err = s.Query(t.Context(), Params{Person: "person"}); !errors.Is(err, ErrRecordsChanged) {
				t.Fatal("query accepted outside change", err)
			}
			reopened, err := Open(root, db)
			if err != nil {
				t.Fatal("outside change prevented Core startup", err)
			}
			defer reopened.Close()
			if _, _, err = reopened.Read(t.Context(), c.ID); !errors.Is(err, ErrRecordsChanged) {
				t.Fatal("reopen adopted outside change", err)
			}
			if after := indexDump(t, db); before != after {
				t.Fatal("outside change replaced approved index")
			}
			if after := git(t, root, "rev-parse", "HEAD"); refBefore != after {
				t.Fatal("outside change altered repository history")
			}
		})
	}
}
