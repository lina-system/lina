// SPDX-License-Identifier: Apache-2.0
package manifest

import (
	"encoding/base64"
	"errors"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// SQLite identifies both the Go binding and the SQLite engine it executes.
// ModuleSum is Go's h1 module-content digest; WasmSHA256 hashes the engine bytes.
type SQLite struct {
	Module        string     `json:"module"`
	Version       string     `json:"version"`
	ModuleSum     string     `json:"moduleSum"`
	SQLiteVersion string     `json:"sqliteVersion"`
	WasmSHA256    string     `json:"wasmSHA256"`
	Execution     string     `json:"execution"`
	SQLiteVec     *SQLiteVec `json:"sqliteVec"`
}

// SQLiteVec records a custom engine with sqlite-vec linked into its Wasm module.
// SHA256 must match that complete module, not a native shared library.
type SQLiteVec struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

var (
	moduleVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	sqliteVersion = regexp.MustCompile(`^3\.[0-9]+\.[0-9]+$`)
)

func (s SQLite) Validate() error {
	if s.Module != "github.com/ncruces/go-sqlite3" || !moduleVersion.MatchString(s.Version) {
		return errors.New("binding must name the selected module and an exact version")
	}
	digest, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(s.ModuleSum, "h1:"))
	if !strings.HasPrefix(s.ModuleSum, "h1:") || err != nil || len(digest) != 32 {
		return errors.New("moduleSum must be a Go h1 digest")
	}
	if !sqliteVersion.MatchString(s.SQLiteVersion) || !digestPattern.MatchString(s.WasmSHA256) || s.Execution != "wazero" {
		return errors.New("engine must name its SQLite version, SHA-256 and wazero execution mode")
	}
	if s.SQLiteVec != nil && (!moduleVersion.MatchString(s.SQLiteVec.Version) || s.SQLiteVec.SHA256 != s.WasmSHA256) {
		return errors.New("sqliteVec must name an exact version and the complete Wasm module digest")
	}
	return nil
}

// SQLiteBuild reads the linked binding's build information and probes the actual
// embedded engine. The build writer runs it with the same readonly module inputs
// and toolchain as the product binaries, including the digest-checked sqlite-vec
// engine. It fetches no runtime extension.
func SQLiteBuild() (SQLite, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return SQLite{}, errors.New("Go build information unavailable")
	}
	s := SQLite{Module: "github.com/ncruces/go-sqlite3", Execution: "wazero"}
	for _, dep := range info.Deps {
		if dep.Path != s.Module {
			continue
		}
		if dep.Replace != nil {
			return SQLite{}, errors.New("replaced SQLite binding is not supported")
		}
		s.Version, s.ModuleSum = dep.Version, dep.Sum
	}
	var err error
	s.SQLiteVersion, s.WasmSHA256, err = canon.SQLiteEngine()
	if err != nil {
		return SQLite{}, err
	}
	vectorVersion, err := canon.SQLiteVectorVersion()
	if err != nil {
		return SQLite{}, err
	}
	s.SQLiteVec = &SQLiteVec{Version: vectorVersion, SHA256: s.WasmSHA256}
	return s, s.Validate()
}
