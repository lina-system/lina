// SPDX-License-Identifier: Apache-2.0
package storebench

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
)

// Python 3's standard SQLite has dbstat on the supported Linux runners. Only
// allocation metadata is returned. The URI is read-only; any WAL index or temp
// files stay inside the caller's disposable root. Errors never echo paths.
const pageInspector = `import json, pathlib, sqlite3, sys
c = sqlite3.connect(pathlib.Path(sys.argv[1]).as_uri() + '?mode=ro')
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
