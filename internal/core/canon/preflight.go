// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrInspectionChanged = errors.New("canon changed during inspection; retry")

// CheckVersion refuses a newer store before callers create control files. It
// never interprets a newer version's tables, whose shape may have changed.
func CheckVersion(ctx context.Context, path string, maximum int) error {
	version, _, err := inspect(ctx, path, maximum)
	if err != nil {
		return err
	}
	if version > maximum {
		return ErrNewerSchema
	}
	return nil
}

// CheckSchema inspects without a writer, migration, lock, or source WAL index.
// It rejects unstable observations, including committed WAL changes, so callers
// without Core's lock cannot mistake a moving store for a compatible snapshot.
func CheckSchema(ctx context.Context, path string, migrations []Migration) (version int, err error) {
	for i, migration := range migrations {
		if migration.Version != i+1 || migration.SQL == "" {
			return 0, ErrInvalid
		}
	}
	before, err := schemaFiles(ctx, path)
	if err != nil {
		return 0, err
	}
	defer func() {
		after, checkErr := schemaFiles(ctx, path)
		if checkErr != nil {
			err = errors.Join(err, checkErr)
			return
		}
		for i := range before {
			if !before[i].equal(after[i]) {
				err = errors.Join(err, ErrInspectionChanged)
				return
			}
		}
	}()
	if before[0].info == nil {
		for _, file := range before[1:] {
			if file.info != nil {
				return 0, ErrRecoveryRequired
			}
		}
		return 0, nil
	}
	if before[2].info != nil && before[2].info.Size() != 0 {
		return 0, ErrRecoveryRequired
	}
	version, _, err = inspect(ctx, path, len(migrations))
	if err != nil {
		return version, err
	}
	if version > len(migrations) {
		return version, ErrNewerSchema
	}
	if err := validatePublishedCatalog(ctx, path, version, migrations); err != nil {
		return version, err
	}
	db, close, err := openInspection(ctx, path)
	if err != nil {
		return version, err
	}
	defer close()
	var metadata, tables int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='canon_meta'").Scan(&metadata); err != nil {
		return version, err
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return version, err
	}
	if metadata == 0 && (tables != 0 || version != 0) {
		return version, ErrInvalid
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return version, err
	}
	if integrity != "ok" {
		return version, ErrInvalid
	}
	return version, nil
}

type schemaFile struct {
	info os.FileInfo
	hash [32]byte
}

func (f schemaFile) equal(other schemaFile) bool {
	if f.info == nil || other.info == nil {
		return f.info == nil && other.info == nil
	}
	return os.SameFile(f.info, other.info) && f.info.Mode() == other.info.Mode() && f.info.Size() == other.info.Size() && f.info.ModTime().Equal(other.info.ModTime()) && f.hash == other.hash
}
func schemaFiles(ctx context.Context, path string) ([3]schemaFile, error) {
	var files [3]schemaFile
	for i, suffix := range []string{"", "-wal", "-journal"} {
		if err := ctx.Err(); err != nil {
			return files, err
		}
		name := path + suffix
		info, err := os.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return files, err
		}
		if !info.Mode().IsRegular() {
			return files, fmt.Errorf("%s: canon must be regular, without links", filepath.Base(name))
		}
		file, err := os.Open(name)
		if err != nil {
			return files, err
		}
		opened, err := file.Stat()
		if err != nil || !os.SameFile(info, opened) {
			file.Close()
			return files, ErrInspectionChanged
		}
		hash := sha256.New()
		_, readErr := io.CopyN(hash, file, info.Size())
		if err := errors.Join(readErr, file.Close()); err != nil {
			return files, err
		}
		files[i] = schemaFile{info: info, hash: [32]byte(hash.Sum(nil))}
	}
	return files, nil
}
