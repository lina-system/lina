// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"bytes"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/secret"
)

func hermesFixture(t *testing.T, name string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".sqlite")
	db, err := canon.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.ExecContext(t.Context(), string(fixture(t, name+".sql"))); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func TestRegisteredSQLiteSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  SQLiteSource
		field string
	}{
		{"lcm", HermesLCM, "tool_calls"}, {"state", HermesState, "reasoning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, db := hermesFixture(t, tc.name)
			before, wal := fileBytes(t, path), fileBytes(t, path+"-wal")
			dest := filepath.Join(t.TempDir(), "snapshot.sqlite")
			if err := SnapshotSQLite(t.Context(), path, dest, tc.kind); err != nil {
				t.Fatal(err)
			}
			snapshot, err := canon.OpenSQLiteReader(t.Context(), dest)
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.Close()
			// Reconcile every source table and view, including FTS shadow tables, against
			// the staged copy, without learning the gate's expected descriptors from it.
			rows, err := db.Query("SELECT name FROM sqlite_schema WHERE type IN ('table','view') AND name NOT GLOB 'sqlite_*' ORDER BY name")
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for rows.Next() {
				var name string
				if err = rows.Scan(&name); err != nil {
					t.Fatal(err)
				}
				names = append(names, name)
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			for _, name := range names {
				quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
				var sourceCount, copyCount int
				if err = db.QueryRow("SELECT count(*) FROM " + quoted).Scan(&sourceCount); err != nil {
					t.Fatal(err)
				}
				if err = snapshot.QueryRow("SELECT count(*) FROM " + quoted).Scan(&copyCount); err != nil {
					t.Fatal(err)
				}
				if sourceCount != copyCount {
					t.Fatalf("%s counts: %d != %d", name, sourceCount, copyCount)
				}
			}
			var preserved string
			if err = snapshot.QueryRow("SELECT " + tc.field + " FROM messages WHERE role='assistant'").Scan(&preserved); err != nil || !strings.Contains(preserved, "synthetic") {
				t.Fatalf("preserved field=%q err=%v", preserved, err)
			}
			if !bytes.Equal(before, fileBytes(t, path)) || !bytes.Equal(wal, fileBytes(t, path+"-wal")) {
				t.Fatal("source bytes changed")
			}
		})
	}
}

func TestRegisteredSQLiteRefusesSchemaDrift(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind SQLiteSource
	}{{"lcm", HermesLCM}, {"state", HermesState}} {
		for _, change := range []string{"PRAGMA user_version=1", "ALTER TABLE messages ADD COLUMN future_field TEXT", "CREATE VIEW unexpected_view AS SELECT role FROM messages"} {
			t.Run(tc.name+"/"+change, func(t *testing.T) {
				path, db := hermesFixture(t, tc.name)
				if _, err := db.Exec(change); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(t.TempDir(), "refused.sqlite")
				if err := SnapshotSQLite(t.Context(), path, dest, tc.kind); !errors.Is(err, ErrSchema) {
					t.Fatalf("drift accepted: %v", err)
				}
				if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("refusal published a snapshot")
				}
			})
		}
	}
	path, _ := hermesFixture(t, "lcm")
	for _, kind := range []SQLiteSource{HermesState, "unknown", ""} {
		if err := SnapshotSQLite(t.Context(), path, filepath.Join(t.TempDir(), "refused"), kind); !errors.Is(err, ErrSchema) {
			t.Fatalf("kind=%q err=%v", kind, err)
		}
	}
}

func TestRegisteredHostViewsAreExact(t *testing.T) {
	path, db := hermesFixture(t, "state")
	if _, err := db.Exec("DROP VIEW messages_fts_src; CREATE VIEW messages_fts_src AS SELECT id, content, tool_name, tool_calls, role FROM messages"); err != nil {
		t.Fatal(err)
	}
	err := SnapshotSQLite(t.Context(), path, filepath.Join(t.TempDir(), "refused"), HermesState)
	if !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), "messages_fts_src") {
		t.Fatalf("changed known view accepted: %v", err)
	}
}

func TestUnregisteredHonchoRefusedBeforeNetwork(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write(fixture(t, "openapi-synthetic.json")) }))
	defer srv.Close()
	reader, err := OpenHoncho(t.Context(), srv.URL, "/openapi.json", secret.New("synthetic-read-credential"))
	if reader != nil {
		reader.Close()
	}
	if !errors.Is(err, ErrSchema) || calls != 0 {
		t.Fatalf("unregistered Honcho err=%v network calls=%d", err, calls)
	}
}
