// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"fmt"
	"math/bits"
	"os"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Crash tests repeatedly exec this test binary. Share only compiled SQLite
// code with those children, never connections, database files or test results.
// The parent's private directory is removed after all tests join their children.
func TestMain(m *testing.M) {
	const key = "LINA_BOOT_TEST_SQLITE_CACHE"
	directory := os.Getenv(key)
	owner := directory == ""
	if owner {
		var err error
		directory, err = os.MkdirTemp("", "lina-boot-code-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err = os.Setenv(key, directory); err != nil {
			os.RemoveAll(directory)
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	cache, err := wazero.NewCompilationCacheWithDir(directory)
	if err != nil {
		if owner {
			os.RemoveAll(directory)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Match go-sqlite3's default runtime limits/features exactly. Canon still
	// verifies the Wasm digest before Initialize selects the compiled module.
	pages := uint32(4096)
	if bits.UintSize < 64 {
		pages = 512
	}
	sqlite3.RuntimeConfig = wazero.NewRuntimeConfig().WithMemoryLimitPages(pages).WithCoreFeatures(api.CoreFeaturesV2).WithCompilationCache(cache)
	code := m.Run()
	if err := cache.Close(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	if owner {
		if err := os.RemoveAll(directory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}
