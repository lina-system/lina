// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func fixtureAt(t *testing.T, root string) (Config, *assets.Store) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "canon"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon/canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	store, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return Config{Root: root, Canon: db, Controls: ctl, MainID: "main-test", Epoch: 1, Artifacts: map[string]string{"lina": "test"}}, store
}
func fixture(t *testing.T) (Config, *assets.Store) { t.Helper(); return fixtureAt(t, t.TempDir()) }
func material(t *testing.T, s *assets.Store) assets.Asset {
	t.Helper()
	token, err := s.StageImport(t.Context(), strings.NewReader("synthetic material"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Handover(t.Context(), token, "note.txt", assets.User, assets.Actor{PersonID: "person-test", AgentID: "agent-test"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func noGeneration(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(root, "backups", e.Name(), "manifest.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("published failed generation: %s (%v)", e.Name(), err)
			}
		}
	}
}
func TestGenerationAndHashMismatch(t *testing.T) {
	t.Parallel()
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "corrupt material"}[corrupt], func(t *testing.T) {
			cfg, store := fixture(t)
			a := material(t, store)
			for _, area := range []string{"workspace", "secrets", "work", "staging/import", "system"} {
				if err := os.MkdirAll(filepath.Join(cfg.Root, area), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg.Root, area, "canary"), []byte(area), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if corrupt {
				if err := os.WriteFile(filepath.Join(cfg.Root, a.Path), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			m, err := Create(t.Context(), cfg)
			if corrupt {
				if !errors.Is(err, assets.ErrHashMismatch) {
					t.Fatalf("backup error=%v", err)
				}
				noGeneration(t, cfg.Root)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(cfg.Root, "backups", m.GenerationID)
			if _, err := Verify(t.Context(), dest); err != nil {
				t.Fatal(err)
			}
			if m.MainID != cfg.MainID || m.Epoch != "1" || len(m.Assets) != 1 || m.SQLite.Hash == "" || m.SchemaVersion != 1 {
				t.Fatalf("incomplete manifest: %+v", m)
			}
			for _, area := range []string{"secrets", "work", "staging", "system", "backups"} {
				if _, err := os.Stat(filepath.Join(dest, area)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("excluded area %s captured: %v", area, err)
				}
			}
			if body, err := os.ReadFile(filepath.Join(dest, "workspace/canary")); err != nil || string(body) != "workspace" {
				t.Fatalf("workspace missing: %s %v", body, err)
			}
			if err := os.WriteFile(filepath.Join(dest, a.Path), []byte("broken copy"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(t.Context(), dest); err == nil {
				t.Fatal("corrupt published copy verified")
			}
		})
	}
}
func TestFailureBeforePublication(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"removals", "snapshot", "copy", "verify", "publish"} {
		t.Run(stage, func(t *testing.T) {
			cfg, s := fixture(t)
			material(t, s)
			fault := errors.New("injected failure")
			cfg.boundary = func(at string) error {
				if at == stage {
					return fault
				}
				return nil
			}
			if _, err := Create(t.Context(), cfg); !errors.Is(err, fault) {
				t.Fatalf("failure=%v", err)
			}
			noGeneration(t, cfg.Root)
		})
	}
}
func TestMalformedRemovalsPreventCopy(t *testing.T) {
	t.Parallel()
	cfg, s := fixture(t)
	material(t, s)
	if err := os.MkdirAll(filepath.Join(cfg.Root, "backups"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Root, "backups/removals"), []byte("torn\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.boundary = func(stage string) error {
		if stage != "removals" {
			t.Fatalf("copied before successful removal replay: %s", stage)
		}
		return nil
	}
	if _, err := Create(t.Context(), cfg); err == nil {
		t.Fatal("malformed removals accepted")
	}
	noGeneration(t, cfg.Root)
}
