// SPDX-License-Identifier: Apache-2.0
package storebench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Python 3's standard SQLite has dbstat on the supported Linux runners. Only
// allocation metadata is returned. The URI is read-only; any WAL index or temp
// files stay inside the caller's disposable root. Errors never echo paths.
//
// sqlite3.connect parses a file: URI by default only on builds compiled with
// SQLITE_USE_URI; elsewhere it opens the literal string as a filename. uri=True
// asks for URI handling on every build.
const pageInspector = `import json, pathlib, sqlite3, sys
c = sqlite3.connect(pathlib.Path(sys.argv[1]).as_uri() + '?mode=ro', uri=True)
c.execute('PRAGMA query_only=ON')
c.execute('PRAGMA temp_store=MEMORY')
tables = {}
index_bytes = 0
for name, size, owner in c.execute('SELECT d.name,sum(d.pgsize),coalesce(s.tbl_name,d.name) FROM dbstat d LEFT JOIN sqlite_schema s ON s.name=d.name GROUP BY d.name ORDER BY d.name'):
    tables[name] = size
    if any(part in owner for part in ('search', 'fts', 'bigram', 'recall')):
        index_bytes += size
page_size = c.execute('PRAGMA page_size').fetchone()[0]
result = dict(table_bytes=tables, index_bytes=index_bytes,
    database_bytes=c.execute('PRAGMA page_count').fetchone()[0] * page_size,
    free_bytes=c.execute('PRAGMA freelist_count').fetchone()[0] * page_size)
c.close()
print(json.dumps(result, sort_keys=True))
`

func measurePages(ctx context.Context, root string, r *report) error {
	path, err := filepath.Abs(filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		return errors.New("storebench_dbstat_unavailable")
	}
	cmd := exec.CommandContext(ctx, "python3", "-I", "-c", pageInspector, path)
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return errors.New("storebench_dbstat_unavailable")
	}
	var sizes report
	if err := json.Unmarshal(data, &sizes); err != nil || len(sizes.TableBytes) == 0 || sizes.DatabaseBytes == 0 {
		return errors.New("storebench_dbstat_invalid")
	}
	r.TableBytes, r.IndexBytes, r.DatabaseBytes, r.FreeBytes = sizes.TableBytes, sizes.IndexBytes, sizes.DatabaseBytes, sizes.FreeBytes
	return nil
}

func TestPageInspectorReadOnly(t *testing.T) {
	// The URI is built from the path, so a root with URI metacharacters must
	// still resolve to the database file.
	root := filepath.Join(t.TempDir(), "space and #hash")
	dir := filepath.Join(root, "canon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "canon.sqlite")
	db, err := canon.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE notes(body TEXT)`,
		`INSERT INTO notes VALUES('first'),('second')`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := digest(path)
	if err != nil {
		t.Fatal(err)
	}
	var r report
	if err := measurePages(t.Context(), root, &r); err != nil {
		t.Fatal(err)
	}
	after, err := digest(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.TableBytes["notes"] == 0 || r.DatabaseBytes < r.TableBytes["notes"] || r.FreeBytes < 0 {
		t.Fatalf("inspector sizes %+v", r)
	}
	if before != after {
		t.Fatal("inspector changed the database")
	}
}

func TestPageInspectorUnavailableIsPathFree(t *testing.T) {
	var r report
	err := measurePages(t.Context(), t.TempDir(), &r)
	if err == nil || err.Error() != "storebench_dbstat_unavailable" {
		t.Fatalf("missing database: %v", err)
	}
}
