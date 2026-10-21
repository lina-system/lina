// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestCommandValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"--canon", "db"}, {"--canon", "db", "--socket", "sock", "--poll", "0s"}, {"--canon", "db", "--socket", "sock", "unexpected"}} {
		if code := run(t.Context(), args, new(bytes.Buffer)); code != 64 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
	if code := run(t.Context(), []string{"--canon", filepath.Join(t.TempDir(), "absent"), "--socket", "absent", "--once"}, new(bytes.Buffer)); code != 69 {
		t.Fatal(code)
	}
}
