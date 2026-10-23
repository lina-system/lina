// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

const testJournal = "system/crw-takeover-journal.json"

func TestOperatorJournalBackupInventory(t *testing.T) {
	cfg, _ := fixture(t)
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cfg.Root, path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(testJournal, "opaque reservations")
	write("system/operator-private", "excluded")
	m, err := Create(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range m.Files {
		if f.Path == testJournal {
			found = true
			if f.Mode != 0600 || f.Hash == "" {
				t.Fatal("journal not private and verified", f)
			}
		} else if strings.HasPrefix(f.Path, "system/") {
			t.Fatal("captured unrelated system state", f)
		}
	}
	if !found {
		t.Fatal("operator journal missing from generation inventory")
	}
	generation := filepath.Join(cfg.Root, "backups", m.GenerationID)
	if err := os.WriteFile(filepath.Join(generation, testJournal), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(t.Context(), generation); !errors.Is(err, ErrHashMismatch) {
		t.Fatal("journal corruption accepted", err)
	}
}

func TestOperatorJournalRestoreGenerationAndInterruption(t *testing.T) {
	for _, captured := range []bool{true, false} {
		for _, stop := range []string{"", "pending", "saved:" + testJournal, "installed:" + testJournal, "installed:canon"} {
			name := "pre-import/"
			if captured {
				name = "post-import/"
			}
			t.Run(name+stop, func(t *testing.T) {
				cfg, assets := fixture(t)
				if captured {
					if err := os.WriteFile(filepath.Join(cfg.Root, testJournal), []byte("captured reservations"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				m, err := Create(t.Context(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				assets.Close()
				cfg.Canon.Close(context.Background())
				cfg.Controls.(*ctlfiles.Store).Close()
				restoreTarget(t, cfg.Root, "45")
				if err := os.WriteFile(filepath.Join(cfg.Root, testJournal), []byte("later reservations"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg.Root, "system/keep"), []byte("local operator state"), 0600); err != nil {
					t.Fatal(err)
				}
				interrupted := errors.New("interrupted")
				options := RestoreConfig{Root: cfg.Root, Generation: filepath.Join(cfg.Root, "backups", m.GenerationID), Artifacts: cfg.Artifacts, Migrations: canon.Migrations(), boundary: func(at string) error {
					if at == stop {
						return interrupted
					}
					return nil
				}}
				_, err = Restore(t.Context(), options)
				if stop != "" {
					if !errors.Is(err, interrupted) || !errors.Is(CheckRestore(cfg.Root), ErrRestorePending) {
						t.Fatalf("missing interruption/fence: %v", err)
					}
					options.boundary = nil
					if stop == "saved:"+testJournal {
						// An outside write during recovery is not ours to replace.
						path := filepath.Join(cfg.Root, testJournal)
						if err := os.WriteFile(path, []byte("outside edit"), 0600); err != nil {
							t.Fatal(err)
						}
						if _, err := Restore(t.Context(), options); !errors.Is(err, ErrRestorePending) {
							t.Fatal("overwrote a reappeared journal", err)
						}
						data, err := os.ReadFile(path)
						if err != nil || string(data) != "outside edit" {
							t.Fatal("lost outside edit", err)
						}
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					}
					_, err = Restore(t.Context(), options)
				}
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(cfg.Root, testJournal))
				if captured && (err != nil || string(data) != "captured reservations") {
					t.Fatalf("restored mismatched journal: %q %v", data, err)
				}
				if !captured && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("pre-import restore retained later reservations: %q %v", data, err)
				}
				data, err = os.ReadFile(filepath.Join(cfg.Root, "system/keep"))
				if err != nil || string(data) != "local operator state" {
					t.Fatal("restore replaced unrelated system state", err)
				}
				if err := CheckRestore(cfg.Root); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestOperatorJournalRejectsInvalidPaths(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "executable"} {
		t.Run(kind, func(t *testing.T) {
			cfg, _ := fixture(t)
			path := filepath.Join(cfg.Root, testJournal)
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink":
				err = os.Symlink("core/core.lock", path)
			case "executable":
				err = os.WriteFile(path, []byte("journal"), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Create(t.Context(), cfg); err == nil {
				t.Fatal("accepted invalid operator journal")
			}
			noGeneration(t, cfg.Root)
		})
	}
}

func TestRestoreLayoutAllowsOnlyOperatorJournalInSystem(t *testing.T) {
	for _, path := range []string{testJournal, "system/core/epoch", "system/other.json", testJournal + "/nested"} {
		m := Manifest{Directories: []string{".", "system"}, Files: []File{{Path: path, Mode: 0600}}}
		if err := validateRestoreLayout(m); (err == nil) != (path == testJournal) {
			t.Fatalf("layout %s: %v", path, err)
		}
	}
}
