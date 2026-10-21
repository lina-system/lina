// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/ncruces/go-sqlite3"
)

var sqliteModule struct {
	sync.Mutex
	started bool
}

// UseSQLiteWASM selects an optional custom SQLite engine before any connection
// opens. sqlite-vec must be linked into a compatible Wasm engine, not loaded as
// a native shared library. expectedSHA256 comes from the build's manifest.
// Verification and execution use the same byte snapshot. All Core consumers
// must initialize SQLite through this package, before starting storage services.
func UseSQLiteWASM(path, expectedSHA256 string) error {
	sqliteModule.Lock()
	defer sqliteModule.Unlock()
	if sqliteModule.started {
		return errors.New("SQLite engine is already initialized")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != expectedSHA256 {
		return errors.New("SQLite Wasm digest mismatch")
	}
	sqlite3.Binary = data
	sqlite3.Path = ""
	return nil
}

func initializeSQLite() error {
	sqliteModule.Lock()
	defer sqliteModule.Unlock()
	sqliteModule.started = true
	return sqlite3.Initialize()
}

// SQLiteEngine reports the actual engine version and SHA-256 of its Wasm bytes.
// The default engine is embedded in the pinned binding and needs no runtime file.
func SQLiteEngine() (version, digest string, err error) {
	if err := initializeSQLite(); err != nil {
		return "", "", err
	}
	conn, err := sqlite3.Open(":memory:")
	if err != nil {
		return "", "", err
	}
	defer conn.Close()
	stmt, _, err := conn.Prepare("SELECT sqlite_version()")
	if err != nil {
		return "", "", err
	}
	if stmt.Step() {
		version = stmt.ColumnText(0)
	}
	if err := stmt.Close(); err != nil {
		return "", "", err
	}
	return version, fmt.Sprintf("%x", sha256.Sum256(sqlite3.Binary)), nil
}
