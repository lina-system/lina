// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestDaemonReportsSnapshotFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canon.sqlite")
	// SQLite can open this empty database, but it has no readable job snapshot.
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	code := run(ctx, []string{"--canon", path, "--socket", "unused", "--poll", "1ms"}, &stderr)
	if code != 69 || strings.TrimSpace(stderr.String()) != "job_unavailable" {
		t.Fatalf("snapshot failure looked healthy: code=%d stderr=%q", code, stderr.String())
	}
}
