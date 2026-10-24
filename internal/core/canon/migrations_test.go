// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"path/filepath"
	"testing"
)

func TestPublishedMigrationTablesMatchSQL(t *testing.T) {
	t.Parallel()
	db, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Record when each table first exists, so a later migration cannot claim
	// a table created by an earlier one. Execute the SQL with its dependencies.
	catalog := Migrations()
	createdAt := make(map[string]int)
	for _, migration := range catalog {
		if _, err := db.ExecContext(t.Context(), migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		rows, err := db.QueryContext(t.Context(), "SELECT name FROM sqlite_schema WHERE type='table'")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var table string
			if err := rows.Scan(&table); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if _, exists := createdAt[table]; !exists {
				createdAt[table] = migration.Version
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(publishedMigrationTables) == 0 {
		t.Fatal("published migration table catalog is empty")
	}
	for _, entry := range publishedMigrationTables {
		t.Run(entry.table, func(t *testing.T) {
			if entry.version < 1 || entry.version > len(catalog) || catalog[entry.version-1].Version != entry.version {
				t.Fatalf("invalid migration version %d", entry.version)
			}
			if got := createdAt[entry.table]; got != entry.version {
				t.Fatalf("table first created by migration %d, catalog declares %d", got, entry.version)
			}
		})
	}
}
