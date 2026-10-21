// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/vfs"
)

// openInspection must run while Core's lock excludes other writers. It opens
// source files strictly read-only. A private temporary WAL index lets SQLite
// read/recover committed WAL pages without changing the source -shm file.
// It does not copy the database or WAL, and does not use immutable with live WAL.
func openInspection(ctx context.Context, path string) (*sql.DB, func(), error) {
	if err := initializeSQLite(); err != nil {
		return nil, nil, err
	}
	uri, err := databaseURI(path, "ro")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {}
	stat, err := os.Stat(path + "-wal")
	if errors.Is(err, os.ErrNotExist) || err == nil && stat.Size() == 0 {
		uri += "&immutable=1"
	} else if err != nil {
		return nil, nil, err
	} else {
		dir, err := os.MkdirTemp("", "lina-canon-inspect-")
		if err != nil {
			return nil, nil, err
		}
		name := "lina-inspect-" + rand.Text()
		vfs.Register(name, inspectionVFS{VFSFilename: vfs.Find("os").(vfs.VFSFilename), shmPath: filepath.Join(dir, "index")})
		cleanup = func() { vfs.Unregister(name); os.RemoveAll(dir) }
		uri += "&vfs=" + name
	}
	db, err := driver.Open(uri, func(conn *sqlite3.Conn) error { return conn.Exec("PRAGMA query_only=ON") })
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		cleanup()
		return nil, nil, err
	}
	return db, func() { db.Close(); cleanup() }, nil
}

type inspectionVFS struct {
	vfs.VFSFilename
	shmPath string
}

func (v inspectionVFS) Delete(string, bool) error { return sqlite3.READONLY }
func (v inspectionVFS) Open(string, vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	return nil, 0, sqlite3.READONLY
}
func (v inspectionVFS) OpenFilename(name *vfs.Filename, flags vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	if name == nil {
		return nil, 0, sqlite3.READONLY
	}
	flags &^= vfs.OPEN_CREATE | vfs.OPEN_READWRITE | vfs.OPEN_DELETEONCLOSE
	flags |= vfs.OPEN_READONLY
	file, actual, err := v.VFSFilename.OpenFilename(name, flags)
	if err != nil {
		return nil, actual, err
	}
	return &inspectionFile{File: file, shm: vfs.NewSharedMemory(v.shmPath, flags)}, actual, nil
}

type inspectionFile struct {
	vfs.File
	shm vfs.SharedMemory
}

func (f *inspectionFile) SharedMemory() vfs.SharedMemory { return f.shm }
func (f *inspectionFile) Close() error {
	var err error
	if f.shm != nil {
		err = f.shm.Close()
	}
	return errors.Join(err, f.File.Close())
}
