// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/records"
)

func recoveryStore(t *testing.T, root string) (*records.Store, backup.Config, func()) {
	t.Helper()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "canon"), 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := ctl.Write("main.json", []byte(`{"main_id":"import-recovery"}`)); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon/canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := records.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	close := func() { repo.Close(); db.Close(context.Background()); ctl.Close() }
	t.Cleanup(close)
	return repo, backup.Config{Root: root, Canon: db, Controls: ctl, MainID: "import-recovery", Epoch: 1, Artifacts: map[string]string{"lina": "test"}}, close
}

func TestBackupRestoreImportIdentities(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "edited-and-moved"
		if interrupted {
			name = "unacknowledged-publication"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo, cfg, close := recoveryStore(t, root)
			journal := filepath.Join(root, "system/crw-takeover-journal.json")
			data, hash := input(t, `{"source":"tracker","kind":"issue","id":"original","title":"Original task"}`)
			if interrupted {
				repo.PublicationHook = func(at string) error {
					if at == "checkpoint" {
						return errors.New("publication interrupted before acknowledgement")
					}
					return nil
				}
			}
			first, err := Import(t.Context(), repo, actor, data, hash, journal)
			if (err != nil) != interrupted {
				t.Fatal(first, err)
			}
			repo.PublicationHook = nil
			record, revision, err := repo.Read(t.Context(), first.Rows[0].LINAID)
			if err != nil {
				t.Fatal(err)
			}
			if !interrupted {
				record.Path = "resources/moved-task.md"
				record.Data = append(record.Data, []byte("\nLater user decision.\n")...)
				if _, err := repo.Commit(t.Context(), records.Change{ID: record.ID, Base: revision, Path: record.Path, Data: record.Data}, actor); err != nil {
					t.Fatal(err)
				}
			}
			m, err := backup.Create(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			close()
			target := t.TempDir()
			_, _, closeTarget := recoveryStore(t, target)
			closeTarget()
			if _, err := backup.Restore(t.Context(), backup.RestoreConfig{Root: target, Generation: filepath.Join(root, "backups", m.GenerationID), Artifacts: cfg.Artifacts, Migrations: canon.Migrations()}); err != nil {
				t.Fatal(err)
			}
			restored, _, _ := recoveryStore(t, target)
			head, err := restored.Head(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			again, err := Import(t.Context(), restored, actor, data, hash, filepath.Join(target, "system/crw-takeover-journal.json"))
			if err != nil || again.Rows[0].LINAID != first.Rows[0].LINAID || !again.Rows[0].Reused {
				t.Fatalf("restored retry lost reservation: first=%+v retry=%+v err=%v", first, again, err)
			}
			after, err := restored.Head(t.Context())
			if err != nil || after != head {
				t.Fatal("retry created a duplicate commit", err)
			}
			got, _, err := restored.Read(t.Context(), record.ID)
			if err != nil || got.Path != record.Path || !bytes.Equal(got.Data, record.Data) {
				t.Fatal("retry lost later edits or move", got, err)
			}
			if _, _, err := restored.Read(t.Context(), "original"); !errors.Is(err, records.ErrNotFound) {
				t.Fatal("source id resolved", err)
			}
		})
	}
}

func TestPreImportRestoreClearsReservations(t *testing.T) {
	root := t.TempDir()
	repo, cfg, close := recoveryStore(t, root)
	m, err := backup.Create(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(root, "system/crw-takeover-journal.json")
	data, hash := input(t, `{"source":"tracker","kind":"issue","id":"original","title":"Original task"}`)
	first, err := Import(t.Context(), repo, actor, data, hash, journal)
	if err != nil {
		t.Fatal(err)
	}
	close()
	if _, err := backup.Restore(t.Context(), backup.RestoreConfig{Root: root, Generation: filepath.Join(root, "backups", m.GenerationID), Artifacts: cfg.Artifacts, Migrations: canon.Migrations()}); err != nil {
		t.Fatal(err)
	}
	restored, _, _ := recoveryStore(t, root)
	if _, _, err := restored.Read(t.Context(), first.Rows[0].LINAID); !errors.Is(err, records.ErrNotFound) {
		t.Fatal("pre-import restore retained imported record", err)
	}
	again, err := Import(t.Context(), restored, actor, data, hash, journal)
	if err != nil || again.Rows[0].Reused || again.Rows[0].LINAID == first.Rows[0].LINAID {
		t.Fatal("pre-import restore retained later reservations", again, err)
	}
	last, err := Import(t.Context(), restored, actor, data, hash, journal)
	if err != nil || !last.Rows[0].Reused || last.Rows[0].LINAID != again.Rows[0].LINAID {
		t.Fatal("retry after pre-import restore was not idempotent", last, err)
	}
}
