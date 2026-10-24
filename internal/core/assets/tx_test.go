// SPDX-License-Identifier: Apache-2.0
package assets_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestPreparedMaterialSharesCallerCommit(t *testing.T) {
	t.Parallel()
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit"}[commit], func(t *testing.T) {
			s, db, root := setup(t)
			w, err := assets.OpenMaterialWriter(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			p, err := w.Prepare(t.Context(), strings.NewReader("masked body"), "ledger-body", actor)
			if err != nil {
				t.Fatal(err)
			}
			var a assets.Asset
			failure := errors.New("injected rollback")
			err = db.Write(t.Context(), func(tx *canon.Tx) error {
				var err error
				a, err = p.Insert(t.Context(), tx)
				if err != nil {
					return err
				}
				if _, err := s.Get(t.Context(), a.ID); !errors.Is(err, assets.ErrNotFound) {
					t.Fatalf("uncommitted material visible: %v", err)
				}
				if !commit {
					return failure
				}
				return nil
			})
			if commit && err != nil || !commit && !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if commit {
				body, err := s.Read(t.Context(), a.ID, a.Revision)
				if err != nil || string(body) != "masked body" {
					t.Fatalf("committed bytes: %q %v", body, err)
				}
			} else if _, err := os.Stat(filepath.Join(root, a.Path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rolled back bytes retained: %v", err)
			}
			assertPreparedClean(t, root)
			manifest, err := s.Backup(t.Context(), t.TempDir())
			if err != nil || len(manifest) != map[bool]int{false: 0, true: 1}[commit] {
				t.Fatalf("backup: %+v %v", manifest, err)
			}
		})
	}
}

func assertPreparedClean(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "staging/material-writes"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("prepared markers: %v %v", entries, err)
	}
}

// Abruptly terminate the subprocess at each publication boundary, so neither
// PreparedMaterial.Close nor canon's deferred rollback can perform cleanup.
func TestPreparedMaterialCrashRecovery(t *testing.T) {
	t.Parallel()
	if root := os.Getenv("LINA_ASSET_TX_CRASH_ROOT"); root != "" {
		s, db := openCrashStore(t, root, 1)
		w, err := assets.OpenMaterialWriter(t.Context(), s)
		if err != nil {
			t.Fatal(err)
		}
		p, err := w.Prepare(t.Context(), strings.NewReader("masked body"), "ledger-body", actor)
		if err != nil {
			t.Fatal(err)
		}
		phase := os.Getenv("LINA_ASSET_TX_CRASH_PHASE")
		if phase == "prepared" {
			os.Exit(23)
		}
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			if _, err := p.Insert(t.Context(), tx); err != nil {
				return err
			}
			if phase == "in_transaction" {
				os.Exit(23)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		os.Exit(23)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"prepared", "in_transaction", "committed"} {
		t.Run(phase, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPreparedMaterialCrashRecovery$")
			cmd.Env = []string{"LINA_ASSET_TX_CRASH_ROOT=" + root, "LINA_ASSET_TX_CRASH_PHASE=" + phase}
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("crash: %v\n%s", err, output)
			}
			s, _ := openCrashStore(t, root, 2)
			if _, err := assets.OpenMaterialWriter(t.Context(), s); err != nil {
				t.Fatal(err)
			}
			assertPreparedClean(t, root)
			manifest, err := s.Backup(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if phase == "committed" {
				if len(manifest) != 1 {
					t.Fatalf("committed revision lost: %+v", manifest)
				}
				body, err := s.Read(t.Context(), manifest[0].AssetID, 1)
				if err != nil || string(body) != "masked body" {
					t.Fatalf("recovered bytes: %q %v", body, err)
				}
			} else {
				if len(manifest) != 0 {
					t.Fatalf("uncommitted backup: %+v", manifest)
				}
				for _, path := range files(t, root) {
					if strings.HasPrefix(path, "materials/") {
						t.Fatalf("orphan bytes: %s", path)
					}
				}
			}
		})
	}
}

func openCrashStore(t *testing.T, root string, epoch uint64) (*assets.Store, *canon.Store) {
	t.Helper()
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	s, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, db
}
