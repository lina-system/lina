// SPDX-License-Identifier: Apache-2.0
package assets_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/node"
)

func TestOpenCopyPinsIdentityRevisionAndHash(t *testing.T) {
	s, _, _ := setup(t)
	a := importAsset(t, s, "first")
	b := importAsset(t, s, "second")
	updated, err := s.Edit(t.Context(), a.ID, a.Revision, strings.NewReader("edited"), actor)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, id, hash string
		revision       int64
		want           error
	}{
		{"original revision", a.ID, a.Hash, a.Revision, nil},
		{"current revision", a.ID, updated.Hash, updated.Revision, nil},
		{"other asset hash", a.ID, b.Hash, a.Revision, assets.ErrHashMismatch},
		{"other revision hash", a.ID, updated.Hash, a.Revision, assets.ErrHashMismatch},
		{"empty hash", a.ID, "", a.Revision, assets.ErrInvalid},
		{"missing asset", strings.Repeat("0", 32), a.Hash, a.Revision, assets.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := s.OpenCopy(t.Context(), tc.id, tc.revision, tc.hash)
			if !errors.Is(err, tc.want) {
				t.Fatalf("open copy: %v, want %v", err, tc.want)
			}
			if err != nil {
				if r != nil {
					r.Close()
					t.Fatal("refusal exposed bytes")
				}
				return
			}
			defer r.Close()
			if _, err := io.Copy(io.Discard, r); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenCopyRejectsTrashedAndDetectsCorruptBytes(t *testing.T) {
	s, _, root := setup(t)
	a := importAsset(t, s, "original")
	if err := os.WriteFile(filepath.Join(root, a.Path), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := s.OpenCopy(t.Context(), a.ID, a.Revision, a.Hash)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	r.Close()
	if !errors.Is(err, assets.ErrHashMismatch) {
		t.Fatalf("corrupt bytes: %v", err)
	}
	if err := s.Trash(t.Context(), a.ID, actor); err != nil {
		t.Fatal(err)
	}
	r, err = s.OpenCopy(t.Context(), a.ID, a.Revision, a.Hash)
	if r != nil {
		r.Close()
		t.Fatal("trashed copy opened")
	}
	if !errors.Is(err, assets.ErrTrashed) {
		t.Fatalf("trashed copy: %v", err)
	}
}

func TestNodeCopyLeavesCanonicalRevisionUnchanged(t *testing.T) {
	s, _, _ := setup(t)
	body := strings.Repeat("material bytes\n", 1024)
	a := importAsset(t, s, body)
	r, err := s.OpenCopy(t.Context(), a.ID, a.Revision, a.Hash)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	copy, err := node.ReceiveCopy(t.Context(), t.TempDir(), a.Hash, a.Size, r)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if os.Geteuid() != 0 {
		if err := os.WriteFile(copy.Path(), []byte("change"), 0600); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("copy write: %v", err)
		}
	}
	// Even a device owner changing their disposable copy cannot write canon.
	if err := os.Chmod(copy.Path(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copy.Path(), []byte("device-local change"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(t.Context(), a.ID, a.Revision)
	if err != nil || string(got) != body {
		t.Fatalf("canonical bytes changed: %v", err)
	}
	current, err := s.Get(t.Context(), a.ID)
	if err != nil || current != a {
		t.Fatalf("canonical metadata changed: %+v, %v", current, err)
	}
}
