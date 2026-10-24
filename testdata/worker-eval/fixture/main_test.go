// SPDX-License-Identifier: Apache-2.0
package workeval

import (
	"os"
	"testing"
)

// TestMain records that the package's tests ran when the evaluation harness
// supplies a trace path. The file is outside the worktree, so the worker never
// has to create or commit it.
func TestMain(m *testing.M) {
	if path := os.Getenv("WEVAL_TRACE"); path != "" {
		_ = os.WriteFile(path, []byte("go-test\n"), 0o600)
	}
	os.Exit(m.Run())
}
