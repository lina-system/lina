// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func textSQL(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSQLiteWALFullFTS5AndReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "canon ?#%.sqlite")
	db, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatal("writer pool must have one connection")
	}
	// Force new physical connections, proving the policy is applied on each open.
	db.SetMaxIdleConns(0)
	for range 2 {
		if got := textSQL(t, db, "PRAGMA journal_mode"); got != "wal" {
			t.Fatalf("journal_mode=%s", got)
		}
		if got := textSQL(t, db, "PRAGMA synchronous"); got != "2" {
			t.Fatalf("synchronous=%s", got)
		}
	}
	execSQL(t, db, "CREATE VIRTUAL TABLE search USING fts5(body)")
	execSQL(t, db, "INSERT INTO search(body) VALUES (?)", "durable compass")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenSQLiteReader(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if got := textSQL(t, reader, "SELECT body FROM search WHERE search MATCH ?", "compass"); got != "durable compass" {
		t.Fatal(got)
	}
	if got := textSQL(t, reader, "PRAGMA query_only"); got != "1" {
		t.Fatalf("query_only=%s", got)
	}
	if _, err := reader.ExecContext(t.Context(), "INSERT INTO search VALUES ('forbidden')"); err == nil {
		t.Fatal("reader wrote")
	}
	// query_only is defense in depth: the underlying connection is mode=ro too.
	execSQL(t, reader, "PRAGMA query_only=OFF")
	if _, err := reader.ExecContext(t.Context(), "INSERT INTO search VALUES ('forbidden')"); err == nil {
		t.Fatal("read-only open wrote")
	}
}

func TestSQLiteOnlineBackupReadsLiveWAL(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "source.sqlite")
	db, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execSQL(t, db, "PRAGMA wal_autocheckpoint=0")
	execSQL(t, db, "CREATE VIRTUAL TABLE search USING fts5(body)")
	execSQL(t, db, "INSERT INTO search VALUES ('committed compass')")
	if stat, err := os.Stat(path + "-wal"); err != nil || stat.Size() == 0 {
		t.Fatalf("live WAL missing: %v", err)
	}
	reader, err := OpenSQLiteReader(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO search VALUES ('uncommitted compass')"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "backup ?#.sqlite")
	if err := BackupSQLite(t.Context(), reader, destination); err != nil {
		t.Fatal(err)
	}
	backup, err := OpenSQLiteReader(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if got := textSQL(t, backup, "SELECT body FROM search WHERE search MATCH 'compass'"); got != "committed compass" {
		t.Fatal(got)
	}
	if got := textSQL(t, backup, "SELECT count(*) FROM search"); got != "1" {
		t.Fatal(got)
	}
	if got := textSQL(t, backup, "PRAGMA integrity_check"); got != "ok" {
		t.Fatal(got)
	}
	if err := BackupSQLite(t.Context(), reader, destination); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrote destination: %v", err)
	}
	if err := BackupSQLite(t.Context(), reader, path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrote source: %v", err)
	}
}

func TestSQLiteFailures(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	if db, err := OpenSQLiteReader(t.Context(), missing); err == nil {
		db.Close()
		t.Fatal("reader created database")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if db, err := OpenSQLite(ctx, missing); !errors.Is(err, context.Canceled) {
		if db != nil {
			db.Close()
		}
		t.Fatalf("canceled open: %v", err)
	}
	if db, err := OpenSQLite(t.Context(), ""); err == nil {
		db.Close()
		t.Fatal("empty path accepted")
	}
	db, err := OpenSQLite(t.Context(), missing)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	destination := filepath.Join(t.TempDir(), "cancelled.sqlite")
	if err := BackupSQLite(ctx, db, destination); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed backup left output: %v", err)
	}
}
