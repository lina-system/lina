// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func snapshotTo(t *testing.T, source, dest string) {
	t.Helper()
	db, err := canon.OpenSQLiteReader(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = canon.BackupSQLite(t.Context(), db, dest); err != nil {
		t.Fatal(err)
	}
}
func TestRemovalListReplaysBeforeSnapshotOfOlderCanon(t *testing.T) {
	cfg, store := fixture(t)
	ctx := t.Context()
	_, err := ledger.New(cfg.Canon, nil).Admit(ctx, guard.EntryOwner, ledger.Input{ID: "source-test", ConversationID: "conversation-test", PersonID: "person-test", AgentID: "agent-test", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0), TimeQuality: "observed", Body: "synthetic violet preference"})
	if err != nil {
		t.Fatal(err)
	}
	flow := memoryexec.NewMemory(cfg.Canon, nil, nil)
	receipt, err := flow.Remember(ctx, memory.Write{Agent: "agent-test", Person: "person-test", Item: memory.Item{Kind: memory.Preference, Content: "synthetic violet preference", Subject: memory.Subject{Kind: memory.Person, ID: "person-test"}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "source-test", Precision: memory.Exact}}}})
	if err != nil || !receipt.Remembered() {
		t.Fatalf("remember: %+v %v", receipt, err)
	}
	path := filepath.Join(cfg.Root, "canon/canon.sqlite")
	old := filepath.Join(t.TempDir(), "old.sqlite")
	snapshotTo(t, path, old)
	removals, err := retract.Open(ctx, retract.Config{Canon: cfg.Canon, Root: cfg.Root})
	if err != nil {
		t.Fatal(err)
	}
	if err = removals.Forget(ctx, retract.Request{Person: "person-test", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}); err != nil {
		t.Fatal(err)
	}
	removals.Close()
	store.Close()
	if err = cfg.Canon.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Simulate older canon plus the latest, separately retained removal list.
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	snapshotTo(t, old, path)
	cfg.Controls.(interface{ Close() error }).Close()
	cfg, _ = fixtureAt(t, cfg.Root)
	cfg.boundary = func(stage string) error {
		if stage == "snapshot" {
			return cfg.Canon.Read(ctx, func(tx *canon.Tx) error {
				var state string
				if err := tx.QueryRowContext(ctx, "SELECT state FROM memory_item WHERE id=?", receipt.ID).Scan(&state); err != nil {
					return err
				}
				if state != "trashed" {
					t.Fatalf("copy began before replay: %s", state)
				}
				return nil
			})
		}
		return nil
	}
	m, err := Create(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if m.RevocationWatermark != 1 {
		t.Fatalf("watermark=%d", m.RevocationWatermark)
	}
	captured, err := canon.OpenReader(ctx, filepath.Join(cfg.Root, "backups", m.GenerationID, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Close(context.Background())
	hits, err := memory.New(captured).Recall(ctx, memory.RecallScope{Agent: "agent-test", Person: "person-test"}, memory.Query{Text: "violet", At: 100})
	if err != nil || len(hits) != 0 {
		t.Fatalf("backup exposed forgotten item: %+v %v", hits, err)
	}
}
