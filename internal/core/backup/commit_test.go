// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func TestLargePersonaBackup(t *testing.T) {
	t.Parallel()
	cfg, _ := fixture(t)
	store, err := agents.Open(cfg.Root, cfg.Canon)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.Create(t.Context(), agents.Create{
		Name: "Synthetic", PersonID: strings.Repeat("synthetic-owner-", 80000),
		Layers: agents.Layers{Core: "core", DefaultVoice: "voice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Create(t.Context(), cfg)
	if err != nil {
		t.Fatalf("accepted persona cannot be backed up: %v", err)
	}
	if len(m.Repositories) != 1 {
		t.Fatalf("repositories = %v", m.Repositories)
	}
	if _, err := Verify(t.Context(), filepath.Join(cfg.Root, "backups", m.GenerationID)); err != nil {
		t.Fatal(err)
	}
}

func TestCommitIntegrity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raw               string
		tamper, corrupt, cancel bool
		want                    error
	}{
		{name: "valid", raw: "commit 4\x00body"},
		{name: "too short", raw: "commit 3\x00body", want: ErrInvalid},
		{name: "too long", raw: "commit 5\x00body"},
		{name: "negative", raw: "commit -1\x00body", want: ErrInvalid},
		{name: "overflow", raw: "commit 9223372036854775808\x00body", want: ErrInvalid},
		{name: "noncanonical", raw: "commit 04\x00body", want: ErrInvalid},
		{name: "wrong type", raw: "blob 4\x00body", want: ErrInvalid},
		{name: "missing header terminator", raw: "commit 4body"},
		{name: "unbounded header", raw: "commit " + strings.Repeat("1", 10000)},
		{name: "hash mismatch", raw: "commit 4\x00body", tamper: true, want: ErrHashMismatch},
		{name: "zlib checksum", raw: "commit 4\x00body", corrupt: true},
		{name: "cancelled", raw: "commit 4\x00body", cancel: true, want: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			digest := sha1.Sum([]byte(tc.raw))
			head := hex.EncodeToString(digest[:])
			raw := tc.raw
			if tc.tamper {
				raw = "commit 4\x00evil"
			}
			var compressed bytes.Buffer
			writer := zlib.NewWriter(&compressed)
			if _, err := writer.Write([]byte(raw)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			data := compressed.Bytes()
			if tc.corrupt {
				data[len(data)-1] ^= 1
			}
			dir := filepath.Join("objects", head[:2])
			if err := root.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := root.WriteFile(filepath.Join(dir, head[2:]), data, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err = verifyCommit(ctx, root, ".", head)
			if tc.name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("error = %v, want rejection (%v)", err, tc.want)
			}
		})
	}
}
