// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestRestoreReviewPreservesInstalledMigrationNumbers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	old := append([]canon.Migration{}, canon.Migrations()[:11]...)
	for _, schema := range []string{turn.Schema, turn.ControlSchema, turn.OutcomeSchema} {
		old = append(old, canon.Migration{Version: len(old) + 1, SQL: schema})
	}
	config := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: old}
	db, err := canon.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	config.Migrations = canon.Migrations()
	db, err = canon.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("installed version 14 cannot upgrade: %v", err)
	}
	defer db.Close(context.Background())
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var n int
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM sum_node").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
}

func reviewGeneration(t *testing.T) (RestoreConfig, *ctlfiles.Store) {
	t.Helper()
	cfg, store := fixture(t)
	material(t, store)
	cfg.Artifacts["backup_command"] = "build-before"
	m, err := Create(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	cfg.Canon.Close(context.Background())
	cfg.Controls.(*ctlfiles.Store).Close()
	target := filepath.Join(t.TempDir(), "target")
	restoreTarget(t, target, "80")
	ctl, err := ctlfiles.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	return RestoreConfig{Root: target, Generation: filepath.Join(cfg.Root, "backups", m.GenerationID), Artifacts: cfg.Artifacts, Migrations: canon.Migrations()}, ctl
}

func TestRestoreReviewClearsOnlyVerifiedHalt(t *testing.T) {
	t.Parallel()
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "malformed"}[valid], func(t *testing.T) {
			cfg, ctl := reviewGeneration(t)
			if err := ctl.Acquire(); err != nil {
				t.Fatal(err)
			}
			marker := []byte(`{"schema":1,"epoch":"80","cause":"sqlite_corrupt"}`)
			if !valid {
				marker = []byte(`{"schema":100}`)
			}
			if err := ctl.Write("halt.json", marker); err != nil {
				t.Fatal(err)
			}
			ctl.Close()
			if valid {
				cfg.boundary = func(at string) error {
					if at == "installed:canon" {
						return errors.New("interrupted before final verification")
					}
					return nil
				}
				if _, err := Restore(t.Context(), cfg); err == nil {
					t.Fatal("missing interruption")
				}
				if _, err := os.Stat(filepath.Join(cfg.Root, "system/core/halt.json")); err != nil {
					t.Fatal("halt cleared before reconciliation")
				}
				cfg.boundary = nil
			}
			_, err := Restore(t.Context(), cfg)
			if !valid {
				if err == nil {
					t.Fatal("malformed halt marker accepted")
				}
				after, e := os.ReadFile(filepath.Join(cfg.Root, "system/core/halt.json"))
				if e != nil || string(after) != string(marker) {
					t.Fatal("invalid halt marker changed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(cfg.Root, "system/core/halt.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("verified restore retained halt: %v", err)
			}
			epoch, err := os.ReadFile(filepath.Join(cfg.Root, "system/core/epoch"))
			if err != nil || string(epoch) != "80" {
				t.Fatal("epoch changed")
			}
		})
	}
}

func TestRestoreReviewIgnoresOnlyCommandProvenance(t *testing.T) {
	t.Parallel()
	cfg, _ := reviewGeneration(t)
	cfg.Artifacts = map[string]string{"lina": "test", "backup_command": "rebuilt-command"}
	cfg.boundary = func(at string) error {
		if at == "installed:canon" {
			return errors.New("injected")
		}
		return nil
	}
	if _, err := Restore(t.Context(), cfg); err == nil {
		t.Fatal("missing interruption")
	}
	if !errors.Is(CheckRestore(cfg.Root), ErrRestorePending) {
		t.Fatal("compatible command rebuild was rejected before publication")
	}
	cfg.boundary = nil
	cfg.Artifacts["backup_command"] = "rebuilt-again"
	if _, err := Restore(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Artifacts["lina"] = "different-installed-release"
	if _, err := Restore(t.Context(), cfg); err == nil {
		t.Fatal("incompatible installed release accepted")
	}
}

func TestRestoreReviewReplacesCorruptTarget(t *testing.T) {
	t.Parallel()
	for _, fence := range []string{"valid", "missing", "malformed"} {
		t.Run(fence, func(t *testing.T) {
			cfg, _ := reviewGeneration(t)
			if fence == "missing" {
				if err := os.Remove(filepath.Join(cfg.Root, "system/core/epoch")); err != nil {
					t.Fatal(err)
				}
			}
			if fence == "malformed" {
				if err := os.WriteFile(filepath.Join(cfg.Root, "system/core/epoch"), []byte("01"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(cfg.Root, "canon"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfg.Root, "canon/canon.sqlite")
			if err := os.WriteFile(path, []byte("damaged database"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Restore(t.Context(), cfg)
			if fence != "valid" {
				if err == nil {
					t.Fatal("corrupt target restored without durable epoch fence")
				}
				raw, readErr := os.ReadFile(path)
				if readErr != nil || string(raw) != "damaged database" {
					t.Fatal("refused target changed")
				}
				return
			}
			if err != nil {
				t.Fatalf("corrupt canon could not be restored: %v", err)
			}
			if _, epoch, err := canon.Inspect(t.Context(), path); err != nil || epoch != 80 {
				t.Fatalf("epoch=%d err=%v", epoch, err)
			}
		})
	}
}

func TestRestoreReviewChecksNewerTargetGenerations(t *testing.T) {
	t.Parallel()
	cfg, store := fixture(t)
	_, err := ledger.New(cfg.Canon, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "source", ConversationID: "conversation", AgentID: "agent", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "synthetic violet"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := memoryexec.NewMemory(cfg.Canon, nil, nil).Remember(t.Context(), memory.Write{Agent: "agent", Person: "person", Item: memory.Item{Kind: memory.Preference, Content: "synthetic violet", Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "source", Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	old, err := Create(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	archive := t.TempDir()
	source, err := os.OpenRoot(cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := os.OpenRoot(archive)
	if err != nil {
		t.Fatal(err)
	}
	files, dirs := []File{}, []string{}
	err = copyTree(t.Context(), source, dest, "backups", func(string) error { return nil }, &files, &dirs)
	source.Close()
	dest.Close()
	if err != nil {
		t.Fatal(err)
	}
	removals, err := retract.Open(t.Context(), retract.Config{Canon: cfg.Canon, Root: cfg.Root})
	if err != nil {
		t.Fatal(err)
	}
	if err := removals.Forget(t.Context(), retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}); err != nil {
		t.Fatal(err)
	}
	removals.Close()
	if _, err := Create(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	store.Close()
	cfg.Canon.Close(context.Background())
	cfg.Controls.(*ctlfiles.Store).Close()
	if err := os.RemoveAll(filepath.Join(cfg.Root, "canon")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cfg.Root, "backups/removals")); err != nil {
		t.Fatal(err)
	}
	restoreTarget(t, cfg.Root, "80")
	_, err = Restore(t.Context(), RestoreConfig{Root: cfg.Root, Generation: filepath.Join(archive, "backups", old.GenerationID), Artifacts: cfg.Artifacts, Migrations: canon.Migrations()})
	if !errors.Is(err, retract.ErrJournal) {
		t.Fatalf("target's newer forgetting ignored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Root, "canon")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete removal history published canon")
	}
}

func TestRestoreReviewCannotReactivateRetainedCredentials(t *testing.T) {
	t.Parallel()
	db := restoreDB(t)
	vaultRoot := t.TempDir()
	if err := os.Chmod(vaultRoot, 0700); err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.OpenFiles(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	accountStore, err := accounts.Open(t.Context(), db, vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := accounts.Account{ID: "account", Person: "person", Provider: provider.Grok, Label: "Synthetic", Auth: provider.APIKey}
	if err := accountStore.Add(t.Context(), a, accounts.Rotated{Tokens: secrets.NewTokens("synthetic-old-token", "")}); err != nil {
		t.Fatal(err)
	}
	accountStore.Close()
	if err := invalidateAuthority(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	accountStore, err = accounts.Open(t.Context(), db, vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer accountStore.Close()
	for _, status := range []accounts.Status{accounts.OK, accounts.NotEligible, accounts.UsageCapped} {
		if err := accountStore.SetStatus(t.Context(), "account", status); err == nil {
			t.Fatalf("status %s reactivated credentials", status)
		}
	}
	if _, err := accountStore.Credential(t.Context(), "account"); err == nil {
		t.Fatal("restored credentials released")
	}
	if err := accountStore.Replace(t.Context(), "account", accounts.Rotated{Tokens: secrets.NewTokens("synthetic-new-sign-in", "")}); err != nil {
		t.Fatal(err)
	}
	if _, err := accountStore.Credential(t.Context(), "account"); err != nil {
		t.Fatal(err)
	}
}
