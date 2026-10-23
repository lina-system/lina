// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func TestVersionSpaceDerivedAndEmptyDirectories(t *testing.T) {
	t.Parallel()
	cfg, _ := fixture(t)
	store, err := agents.Open(cfg.Root, cfg.Canon)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.FirstRun(t.Context(), "person-test", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"workspace/empty", "derived/source-asset/7"} {
		if err = os.MkdirAll(filepath.Join(cfg.Root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(cfg.Root, "derived/source-asset/7/content"), []byte("synthetic derivation"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("empty", filepath.Join(cfg.Root, "workspace/link")); err != nil {
		t.Fatal(err)
	}
	m, err := Create(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	starters, err := store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Repositories) != 8 || len(starters) != 8 {
		t.Fatalf("repositories: %+v", m.Repositories)
	}
	for _, a := range starters {
		found := false
		for _, repo := range m.Repositories {
			found = found || repo.Path == "versions/agents/"+a.ID && repo.Head != ""
		}
		if !found {
			t.Fatalf("starter repository missing: %s", a.ID)
		}
	}
	if len(m.Derived) != 1 || m.Derived[0].SourceRevision != "7" {
		t.Fatalf("derived: %+v", m.Derived)
	}
	dest := filepath.Join(cfg.Root, "backups", m.GenerationID)
	if _, err := Verify(t.Context(), dest); err != nil {
		t.Fatal(err)
	}
	if link, err := os.Readlink(filepath.Join(dest, "workspace/link")); err != nil || link != "empty" {
		t.Fatalf("symlink: %q %v", link, err)
	}
	if err := os.Remove(filepath.Join(dest, "workspace/empty")); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(t.Context(), dest); err == nil {
		t.Fatal("lost empty directory passed verification")
	}
}
func TestCopyCancellationAndCorruption(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel", "corrupt", "special"} {
		t.Run(mode, func(t *testing.T) {
			cfg, s := fixture(t)
			a := material(t, s)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cfg.boundary = func(stage string) error {
				if stage == "verify" {
					if mode == "cancel" {
						cancel()
					} else {
						entries, _ := os.ReadDir(filepath.Join(cfg.Root, "backups"))
						for _, entry := range entries {
							if entry.IsDir() {
								return os.WriteFile(filepath.Join(cfg.Root, "backups", entry.Name(), a.Path), []byte("changed destination"), 0600)
							}
						}
					}
				}
				return nil
			}
			if mode == "special" {
				if err := os.Remove(filepath.Join(cfg.Root, a.Path)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(cfg.Root, "system/core/core.lock"), filepath.Join(cfg.Root, a.Path)); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Create(ctx, cfg)
			if err == nil {
				t.Fatal("unsafe backup succeeded")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			noGeneration(t, cfg.Root)
		})
	}
}

func TestPostCopyDamageCannotBecomeExpectedHash(t *testing.T) {
	t.Parallel()
	cfg, _ := fixture(t)
	if err := os.Mkdir(filepath.Join(cfg.Root, "workspace"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Root, "workspace/note"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.boundary = func(stage string) error {
		if stage == "copied" {
			entries, err := os.ReadDir(filepath.Join(cfg.Root, "backups"))
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.IsDir() {
					return os.WriteFile(filepath.Join(cfg.Root, "backups", entry.Name(), "workspace/note"), []byte("damaged"), 0600)
				}
			}
		}
		return nil
	}
	if _, err := Create(t.Context(), cfg); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("damaged copy became manifest baseline: %v", err)
	}
	noGeneration(t, cfg.Root)
}
