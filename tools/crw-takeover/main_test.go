// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/backup"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), nil, &stdout, &stderr); code != 64 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatal(code, stdout.String(), stderr.String())
	}
}

func TestPendingRestoreRefusedBeforeOpeningDestination(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"staging/restore.pending/new/canon", "canon", "versions/records"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately not a SQLite database: inspecting it before checking the
	// restore marker must produce the wrong refusal and fail this regression.
	canonPath := filepath.Join(root, "canon/canon.sqlite")
	original := []byte("incomplete mixed generation")
	if err := os.WriteFile(canonPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "snapshot.json")
	data := []byte(`{"version":1,"counts":{"tracker":1},"records":[{"source":"tracker","kind":"issue","id":"old","title":"Original work"}]}`)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"--state-root", root, "--snapshot", source, "--sha256", fmt.Sprintf("%x", sha256.Sum256(data)), "--person", "person", "--agent", "agent"}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), backup.ErrRestorePending.Error()) {
		t.Fatalf("pending restore refusal: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(canonPath)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("canon changed", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "versions/records"))
	if err != nil || len(entries) != 0 {
		t.Fatal("records changed", entries, err)
	}
	if _, err = os.Stat(filepath.Join(root, "system/crw-takeover-journal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reserved imports during restore", err)
	}
	if _, err = os.Stat(filepath.Join(root, "staging/restore.pending")); err != nil {
		t.Fatal("restore marker changed", err)
	}
}
