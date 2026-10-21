// SPDX-License-Identifier: Apache-2.0

// Package canon provides the SQLite binding used by Core's storage services.
// Writer fencing, schema migrations and the writer queue are separate concerns.
package canon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// OpenSQLite opens a file database with one write connection, WAL and FULL
// synchronization. The caller owns the database and serializes its transactions.
// Paths are filesystem paths, never driver URIs or in-memory databases.
func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	return openSQLite(ctx, path, false)
}

// OpenSQLiteReader opens an existing file read-only and sets query_only on every
// connection. It does not change journal mode or create a missing database.
func OpenSQLiteReader(ctx context.Context, path string) (*sql.DB, error) {
	return openSQLite(ctx, path, true)
}

func databaseURI(path, mode string) (string, error) {
	if path == "" {
		return "", errors.New("SQLite path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	q := url.Values{"mode": {mode}}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func openSQLite(ctx context.Context, path string, reader bool) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mode := "rw"
	if reader {
		mode = "ro"
	}
	uri, err := databaseURI(path, mode)
	if err != nil {
		return nil, err
	}
	if err := initializeSQLite(); err != nil {
		return nil, err
	}
	if !reader {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err == nil {
			if err := file.Close(); err != nil {
				return nil, err
			}
		}
	}
	db, err := driver.Open(uri, func(conn *sqlite3.Conn) error {
		if reader {
			if err := installRetractionViews(conn); err != nil {
				return err
			}
			return conn.Exec("PRAGMA query_only=ON")
		}
		stmt, _, err := conn.Prepare("PRAGMA journal_mode=WAL")
		if err != nil {
			return err
		}
		wal := stmt.Step() && stmt.ColumnText(0) == "wal"
		if err := stmt.Close(); err != nil {
			return err
		}
		if !wal {
			return errors.New("SQLite WAL mode unavailable")
		}
		return conn.Exec("PRAGMA synchronous=FULL")
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// BackupSQLite uses sqlite3_backup_init/step/finish to take a consistent online
// snapshot, including committed WAL pages. source can be a read-only connection
// to another application's database. destination must not exist; a failed
// backup removes only the file this call created. It never copies database files.
func BackupSQLite(ctx context.Context, source *sql.DB, destination string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if source == nil {
		return errors.New("SQLite backup source is nil")
	}
	uri, err := databaseURI(destination, "rw")
	if err != nil {
		return err
	}
	conn, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			err = errors.Join(err, os.Remove(destination))
		}
	}()
	err = conn.Raw(func(raw any) error {
		driverConn, ok := raw.(driver.Conn)
		if !ok {
			return errors.New("SQLite backup requires the selected binding")
		}
		sqlite := driverConn.Raw()
		previous := sqlite.SetInterrupt(ctx)
		defer sqlite.SetInterrupt(previous)
		backup, err := sqlite.BackupInit("main", uri)
		if err != nil {
			return err
		}
		for {
			if err := ctx.Err(); err != nil {
				return errors.Join(err, backup.Close())
			}
			done, err := backup.Step(128)
			if err != nil || done {
				return errors.Join(err, backup.Close())
			}
		}
	})
	if err != nil {
		return fmt.Errorf("SQLite online backup: %w", err)
	}
	return file.Sync()
}
