// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math/bits"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/thisisjun786/lina/internal/core/canon"
)

const sqliteCacheEnv = "LINA_E2E_TEST_SQLITE_CACHE"

// Helper processes share compiled code, never databases or acceptance results.
// The parent removes its private directory after all children have joined.
func prepareSQLiteCache() (func() error, error) {
	directory := os.Getenv(sqliteCacheEnv)
	owner := directory == ""
	if owner {
		var err error
		directory, err = os.MkdirTemp("", "lina-e2e-code-")
		if err != nil {
			return nil, err
		}
	}
	remove := func() error {
		if owner {
			return os.RemoveAll(directory)
		}
		return nil
	}
	if err := os.Setenv(sqliteCacheEnv, directory); err != nil {
		return nil, errors.Join(err, remove())
	}
	cache, err := wazero.NewCompilationCacheWithDir(directory)
	if err != nil {
		return nil, errors.Join(err, remove())
	}
	// Retain go-sqlite3's default memory limits and features. Canon verifies
	// the embedded Wasm digest before selecting this compiled module.
	pages := uint32(4096)
	if bits.UintSize < 64 {
		pages = 512
	}
	sqlite3.RuntimeConfig = wazero.NewRuntimeConfig().WithMemoryLimitPages(pages).WithCoreFeatures(api.CoreFeaturesV2).WithCompilationCache(cache)
	return func() error {
		return errors.Join(cache.Close(context.Background()), remove())
	}, nil
}

type cacheObservation struct {
	Directory string
	Digest    string
}

func TestSQLiteCacheChild(t *testing.T) {
	output := os.Getenv("LINA_E2E_CACHE_OBSERVATION")
	if output == "" {
		return
	}
	_, digest, err := canon.SQLiteEngine()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cacheObservation{os.Getenv(sqliteCacheEnv), digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteCacheChildOwnership(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "owned"}[owned], func(t *testing.T) {
			h := newHarness(t, executable)
			output := filepath.Join(h.root, "observation.json")
			child := h.command("-test.run=^TestSQLiteCacheChild$")
			child.command.Env = append(child.command.Env, "LINA_E2E_CACHE_OBSERVATION="+output)
			if owned {
				child.command.Env = append(child.command.Env, sqliteCacheEnv+"=")
			}
			child.start(t)
			child.wait(t, 0)
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var observed cacheObservation
			if err := json.Unmarshal(data, &observed); err != nil {
				t.Fatal(err)
			}
			if observed.Directory == "" || observed.Digest != canon.SQLiteWASMSHA256 {
				t.Fatalf("child did not cache the verified SQLite module: %+v", observed)
			}
			if owned {
				if _, err := os.Stat(observed.Directory); !os.IsNotExist(err) {
					t.Fatalf("child left its owned cache behind: %v", err)
				}
				return
			}
			if observed.Directory != os.Getenv(sqliteCacheEnv) {
				t.Fatal("child did not reuse the parent's compilation cache")
			}
			files := 0
			if err := filepath.WalkDir(observed.Directory, func(_ string, entry fs.DirEntry, err error) error {
				if err == nil && entry.Type().IsRegular() {
					files++
				}
				return err
			}); err != nil || files == 0 {
				t.Fatalf("compiled code was not retained for the next child: files=%d error=%v", files, err)
			}
		})
	}
}
