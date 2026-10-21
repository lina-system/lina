// SPDX-License-Identifier: Apache-2.0

// Package importer reads external memory stores into private import staging.
// It does not write canon, infer schema compatibility, or classify source rows.
package importer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var ErrSchema = errors.New("unknown_import_schema")

// Column describes every table_xinfo column, including generated/hidden ones.
// Default is the SQL default expression, or nil when there is no default.
type Column struct {
	Name       string
	Type       string
	NotNull    bool
	Default    *string
	PrimaryKey int
	Hidden     int
}

// SQLiteSchema is a trusted, independently verified source schema, never a
// descriptor learned from the database being imported. Tables is exhaustive;
// SQLite's own sqlite_* objects are excluded. Column order is significant.
type SQLiteSchema struct {
	Version int
	Tables  map[string][]Column
}

// SnapshotSQLite checks metadata before online backup, then checks the copied
// schema again before exposing the snapshot (a live source may change between
// the two). The destination is a new file under the caller's import staging.
// It must not exist. Failure removes only a destination created by this call.
func SnapshotSQLite(ctx context.Context, source, destination string, known SQLiteSchema) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if len(known.Tables) == 0 {
		return fmt.Errorf("%w: no accepted tables", ErrSchema)
	}
	reader, err := canon.OpenSQLiteReader(ctx, source)
	if err != nil {
		return err
	}
	defer reader.Close()
	if err = checkSQLite(ctx, reader, known); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	if err = outsideSource(filepath.Dir(resolved), destination); err != nil {
		return err
	}
	if err = canon.BackupSQLite(ctx, reader, destination); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(destination))
		}
	}()
	snapshot, err := canon.OpenSQLiteReader(ctx, destination)
	if err != nil {
		return err
	}
	defer snapshot.Close()
	return checkSQLite(ctx, snapshot, known)
}

func checkSQLite(ctx context.Context, db *sql.DB, known SQLiteSchema) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != known.Version {
		return fmt.Errorf("%w: user_version: expected %d, got %d", ErrSchema, known.Version, version)
	}
	rows, err := tx.QueryContext(ctx, "SELECT name, type FROM sqlite_schema WHERE type IN ('table','view') AND name NOT GLOB 'sqlite_*' ORDER BY name")
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return err
		}
		if kind != "table" {
			rows.Close()
			return fmt.Errorf("%w: unexpected view %q", ErrSchema, name)
		}
		names = append(names, name)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	actual := make(map[string]bool, len(names))
	for _, name := range names {
		actual[name] = true
		columns, ok := known.Tables[name]
		if !ok {
			return fmt.Errorf("%w: unexpected table %q", ErrSchema, name)
		}
		rows, err := tx.QueryContext(ctx, "SELECT name,type,\"notnull\",dflt_value,pk,hidden FROM pragma_table_xinfo(?) ORDER BY cid", name)
		if err != nil {
			return err
		}
		var got []Column
		for rows.Next() {
			var c Column
			if err = rows.Scan(&c.Name, &c.Type, &c.NotNull, &c.Default, &c.PrimaryKey, &c.Hidden); err != nil {
				rows.Close()
				return err
			}
			got = append(got, c)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if !reflect.DeepEqual(got, columns) {
			// Name the structural difference without echoing default expressions.
			for i := 0; i < max(len(got), len(columns)); i++ {
				if i >= len(got) {
					return fmt.Errorf("%w: table %q missing column %q", ErrSchema, name, columns[i].Name)
				}
				if i >= len(columns) {
					return fmt.Errorf("%w: table %q unexpected column %q", ErrSchema, name, got[i].Name)
				}
				if !reflect.DeepEqual(got[i], columns[i]) {
					return fmt.Errorf("%w: table %q column %d (%q): name/type/nullability/default/key/hidden differs", ErrSchema, name, i, columns[i].Name)
				}
			}
		}
	}
	var missing []string
	for name := range known.Tables {
		if !actual[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: missing tables %s", ErrSchema, strings.Join(missing, ", "))
	}
	return tx.Commit()
}
