// SPDX-License-Identifier: Apache-2.0
package node_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/node"
)

func digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func TestCopyVerifiedBeforeUseAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	body := "task input\n"
	copy, err := node.ReceiveCopy(t.Context(), dir, digest(body), int64(len(body)), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := copy.Close(); err != nil {
			t.Error(err)
		}
	})
	got, err := os.ReadFile(copy.Path())
	if err != nil || string(got) != body {
		t.Fatalf("copy bytes: %q, %v", got, err)
	}
	info, err := os.Stat(copy.Path())
	if err != nil || info.Mode().Perm() != 0400 {
		t.Fatalf("copy mode: %v, %v", info, err)
	}
	if os.Geteuid() != 0 {
		if err := os.WriteFile(copy.Path(), []byte("changed"), 0600); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("write to copy: %v", err)
		}
	}
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cleanup: %v, %v", entries, err)
	}
}

type failedReader struct{}

func (failedReader) Read(p []byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestCopyFailureDiscardsBytes(t *testing.T) {
	body := "expected"
	for _, tc := range []struct {
		name   string
		reader io.Reader
		hash   string
		size   int64
		cancel bool
	}{
		{"hash mismatch", strings.NewReader("tampered"), digest(body), int64(len(body)), false},
		{"short transfer", strings.NewReader("short"), digest(body), int64(len(body)), false},
		{"long transfer", strings.NewReader(body + "extra"), digest(body), int64(len(body)), false},
		{"transfer failure", failedReader{}, digest(body), int64(len(body)), false},
		{"invalid hash", strings.NewReader(body), "../escape", int64(len(body)), false},
		{"invalid size", strings.NewReader(body), digest(body), -1, false},
		{"cancelled", strings.NewReader(body), digest(body), int64(len(body)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			copy, err := node.ReceiveCopy(ctx, dir, tc.hash, tc.size, tc.reader)
			if copy != nil || err == nil {
				if copy != nil {
					copy.Close()
				}
				t.Fatalf("unverified copy: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed transfer retained bytes: %v, %v", entries, err)
			}
		})
	}
}
