// SPDX-License-Identifier: Apache-2.0
package canon

import (
	_ "embed"

	"github.com/ncruces/go-sqlite3"
)

// These pins identify the complete engine, including the binding's ABI, SQLite
// and sqlite-vec. packaging/sqlite-vec documents its reproducible construction.
const SQLiteVecVersion = "v0.1.6"
const SQLiteWASMSHA256 = "2eaa33d000922ae5369d6fbf55be07ae03f68ebb7bf82ec49e2657282e37718a"

//go:embed sqlite-vec.wasm
var embeddedSQLite []byte

func init() {
	sqlite3.Binary = embeddedSQLite
}
