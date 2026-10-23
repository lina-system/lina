// SPDX-License-Identifier: Apache-2.0

// Package hermes is the optional Hermes memory importer add-on.
// Snapshot readers never write a source or infer schema compatibility. Prepared
// import plans write staged canon through its fenced writer after matching.
package hermes

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
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
// descriptor learned from the database being imported. Tables and Views are
// exhaustive; SQLite's own sqlite_* objects are excluded. Column order matters.
type SQLiteSchema struct {
	Version int
	Tables  map[string][]Column
	Views   map[string][]Column
}

// SnapshotSQLite checks metadata before online backup, then checks the copied
// schema again before exposing the snapshot (a live source may change between
// the two). The destination is a new file under the caller's import staging.
// It must not exist. Failure removes only a destination created by this call.
func SnapshotSQLite(ctx context.Context, source, destination string, kind SQLiteSource) error {
	if _, ok := registeredSQLiteSchemas[kind]; !ok {
		return fmt.Errorf("%w: unregistered SQLite source %q", ErrSchema, kind)
	}
	reader, closeSource, err := canon.OpenSQLiteInspection(ctx, source)
	if err != nil {
		return err
	}
	known, err := registeredSchema(ctx, reader, kind)
	closeSource()
	if err != nil {
		return err
	}
	return snapshotSQLite(ctx, source, destination, known)
}

// snapshotSQLite is the descriptor-driven mechanism; only registered layouts
// are exposed by the production entry point above.
func snapshotSQLite(ctx context.Context, source, destination string, known SQLiteSchema) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if len(known.Tables) == 0 {
		return fmt.Errorf("%w: no accepted tables", ErrSchema)
	}
	before, err := sourceFingerprint(ctx, source)
	if err != nil {
		return err
	}
	reader, closeSource, err := canon.OpenSQLiteInspection(ctx, source)
	if err != nil {
		return err
	}
	defer closeSource()
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
	if err = checkSQLite(ctx, snapshot, known); err != nil {
		return err
	}
	after, err := sourceFingerprint(ctx, source)
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("%w: source changed during snapshot; retry when quiet", ErrConflict)
	}
	return nil
}

// Presence only selects a registered variant; the full descriptor comparison
// still rejects every unregistered table, view, column or version.
func registeredSchema(ctx context.Context, db *sql.DB, kind SQLiteSource) (SQLiteSchema, error) {
	known, ok := registeredSQLiteSchemas[kind]
	if !ok {
		return SQLiteSchema{}, fmt.Errorf("%w: unregistered SQLite source %q", ErrSchema, kind)
	}
	if kind == HermesState {
		var present int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='delivery_obligations'").Scan(&present); err != nil {
			return SQLiteSchema{}, err
		}
		if present != 0 {
			var adapter int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_xinfo('delivery_obligations') WHERE name='adapter_profile'").Scan(&adapter); err != nil {
				return SQLiteSchema{}, err
			}
			tables := make(map[string][]Column, len(known.Tables)+1)
			for name, columns := range known.Tables {
				tables[name] = columns
			}
			columns := deliveryColumns
			if adapter == 0 {
				columns = columns[:len(columns)-1]
			}
			tables["delivery_obligations"] = columns
			known.Tables = tables
		}
	}
	return known, checkSQLite(ctx, db, known)
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
	type object struct{ name, kind string }
	var objects []object
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, object{name, kind})
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	actual := make(map[object]bool, len(objects))
	for _, item := range objects {
		name, kind := item.name, item.kind
		actual[item] = true
		expected := known.Tables
		if kind == "view" {
			expected = known.Views
		}
		columns, ok := expected[name]
		if !ok {
			return fmt.Errorf("%w: unexpected %s %q", ErrSchema, kind, name)
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
					return fmt.Errorf("%w: %s %q missing column %q", ErrSchema, kind, name, columns[i].Name)
				}
				if i >= len(columns) {
					return fmt.Errorf("%w: %s %q unexpected column %q", ErrSchema, kind, name, got[i].Name)
				}
				if !reflect.DeepEqual(got[i], columns[i]) {
					return fmt.Errorf("%w: %s %q column %d (%q): name/type/nullability/default/key/hidden differs", ErrSchema, kind, name, i, columns[i].Name)
				}
			}
		}
	}
	var missing []string
	for kind, expected := range map[string]map[string][]Column{"table": known.Tables, "view": known.Views} {
		for name := range expected {
			if !actual[object{name, kind}] {
				missing = append(missing, kind+" "+name)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: missing objects %s", ErrSchema, strings.Join(missing, ", "))
	}
	return tx.Commit()
}

// A private WAL index cannot coordinate with an external writer's index. Prove
// that both source files remained stable across the read; never publish a
// snapshot raced by a checkpoint or commit. SQLite still performs the copy via
// its online backup API, including committed WAL pages.
func sourceFingerprint(ctx context.Context, path string) (string, error) {
	h := sha256.New()
	for _, suffix := range []string{"", "-wal"} {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		f, err := os.Open(path + suffix)
		if suffix != "" && errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(h, "absent")
			continue
		}
		if err != nil {
			return "", err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return "", err
		}
		if !info.Mode().IsRegular() {
			f.Close()
			return "", invalidSnapshot("SQLite source regular file")
		}
		fmt.Fprintf(h, "%s:%d:%d:", suffix, info.Size(), info.ModTime().UnixNano())
		_, err = io.Copy(h, f)
		err = errors.Join(err, f.Close())
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
