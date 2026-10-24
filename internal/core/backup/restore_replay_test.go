// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func TestRestoreReplaysRemovalsBeforeFirstReadInAnotherRoot(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"forget", "delete", "recover"} {
		t.Run(operation, func(t *testing.T) {
			db := restoreDB(t)
			source := t.TempDir()
			removals, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: source})
			if err != nil {
				t.Fatal(err)
			}
			defer removals.Close()
			const body = "synthetic violet preference"
			_, err = ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "source", ConversationID: "conversation", AgentID: "agent", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: body})
			if err != nil {
				t.Fatal(err)
			}
			flow := memoryexec.NewMemory(db, nil, nil)
			write := memory.Write{Agent: "agent", Person: "person", Item: memory.Item{Kind: memory.Preference, Content: body, Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "source", Precision: memory.Exact}}}}
			receipt, err := flow.Remember(t.Context(), write)
			if err != nil {
				t.Fatal(err)
			}
			tree := compaction.New(db)
			frontier, err := tree.Frontier(t.Context(), "conversation")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tree.Publish(t.Context(), compaction.Proposal{Expected: frontier, ID: "summary", Cover: []compaction.Coverage{{EventID: "source"}}, Output: compaction.Output{Model: "fixture", PromptVersion: "v1", BuiltAt: time.Unix(20, 0), Text: body, Dropped: body}}); err != nil {
				t.Fatal(err)
			}
			// Snapshot the actual SQLite store, then mutate only the current removal list.
			generation := t.TempDir()
			if err := os.Chmod(generation, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(generation, "canon"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(generation, "canon/canon.sqlite")
			// Read-only SQLite snapshot from the store's owned path is obtained through
			// a test fixture pointer, never by copying live database or WAL bytes.
			var dbPath string
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				var sequence int
				var name string
				return tx.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &name, &dbPath)
			}); err != nil {
				t.Fatal(err)
			}
			reader, err := canon.OpenSQLiteReader(t.Context(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := canon.BackupSQLite(t.Context(), reader, path); err != nil {
				t.Fatal(err)
			}
			reader.Close()
			request := retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}
			if err := removals.Forget(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if operation == "delete" {
				if err := removals.Delete(t.Context(), request); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "recover" {
				if err := removals.Restore(t.Context(), request); err != nil {
					t.Fatal(err)
				}
			}
			journal, err := os.ReadFile(filepath.Join(source, "backups/removals"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(generation, "backups"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(generation, "backups/removals"), journal, 0600); err != nil {
				t.Fatal(err)
			}
			if err := prepareRestoredCanon(t.Context(), generation, 20, canon.Migrations(), nil); err != nil {
				t.Fatal(err)
			}
			controls, err := ctlfiles.Open(generation)
			if err != nil {
				t.Fatal(err)
			}
			defer controls.Close()
			if err := controls.Acquire(); err != nil {
				t.Fatal(err)
			}
			restored, err := canon.Open(t.Context(), canon.Config{Path: path, Epoch: 20, Controls: controls, Migrations: canon.Migrations()})
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close(context.Background())
			if node, err := compaction.New(restored).Node(t.Context(), "summary"); !errors.Is(err, compaction.ErrUnavailable) || node.Output.Text != "" {
				t.Fatalf("restored stale summary exposed: %+v %v", node, err)
			}
			var summary string
			if err := restored.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT text||dropped FROM sum_node WHERE id='summary'").Scan(&summary)
			}); err != nil {
				t.Fatal(err)
			}
			if summary != "" {
				t.Fatal("revoked summary content retained")
			}
			want := 0
			if operation == "recover" {
				want = 1
			}
			var n int
			if err := restored.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM retract_visible_memory WHERE id=?", receipt.ID).Scan(&n)
			}); err != nil {
				t.Fatal(err)
			}
			if n != want {
				t.Fatalf("first read exposed %d, want %d", n, want)
			}
			hits, err := memory.New(restored).Recall(t.Context(), memory.RecallScope{Agent: "agent", Person: "person"}, memory.Query{Text: "violet", At: 100})
			if err != nil || len(hits) != want {
				t.Fatalf("recall exposed %d: %v", len(hits), err)
			}
			events, err := ledger.New(restored, nil).Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "person", Text: "violet"})
			if err != nil || len(events) != want {
				t.Fatalf("ledger fallback exposed %d: %v", len(events), err)
			}
			// The replay also reaches the shared index before the first read: the
			// generation was captured with the item indexed, and a recovered item
			// waits in the queue instead of reappearing on its own.
			indexed := func() int {
				t.Helper()
				var n int
				if err := restored.Read(t.Context(), func(tx *canon.Tx) error {
					return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_doc WHERE kind='memory' AND source_id=?", receipt.ID).Scan(&n)
				}); err != nil {
					t.Fatal(err)
				}
				return n
			}
			if n := indexed(); n != 0 {
				t.Fatalf("restored index exposed the hidden item: %d", n)
			}
			if operation == "recover" {
				drain := recall.New(restored, nil)
				for range 10 {
					var pending int
					if err := restored.Read(t.Context(), func(tx *canon.Tx) error {
						return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_pending").Scan(&pending)
					}); err != nil {
						t.Fatal(err)
					}
					if pending == 0 {
						break
					}
					if _, err := drain.DrainBatch(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				if n := indexed(); n != 1 {
					t.Fatalf("recovered item was not re-queued: %d", n)
				}
			}
		})
	}
}

func TestRestoreRemovalListsNeverDiscardNewerHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		source, target, want string
		invalid              bool
	}{
		{"a\n", "a\nb\n", "a\nb\n", false}, {"a\nb\n", "a\n", "a\nb\n", false}, {"a\n", "", "a\n", false}, {"a\n", "b\n", "", true}, {"a", "a\n", "", true},
	} {
		got, err := mergeRestoreRemovals([]byte(tc.source), []byte(tc.target))
		if tc.invalid {
			if err == nil {
				t.Fatal("divergent or truncated removal history accepted")
			}
		} else if err != nil || string(got) != tc.want {
			t.Fatalf("merge: %q %v", got, err)
		}
	}
}

func TestRestoreRefusesMissingRemovalList(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "canon"), 0700); err != nil {
		t.Fatal(err)
	}
	// Even a generation with no acknowledged removals must travel with a list;
	// silently inventing an empty list could lose a post-generation forgetting.
	if err := prepareRestoredCanon(t.Context(), root, 20, canon.Migrations(), nil); err == nil {
		t.Fatal("restore invented an empty removal list")
	}
	if _, err := os.Stat(filepath.Join(root, "canon/canon.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing removal list changed canon")
	}
}

func TestRestoreReplaysAJournaledRepair(t *testing.T) {
	t.Parallel()
	db := restoreDB(t)
	source := t.TempDir()
	removals, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: source})
	if err != nil {
		t.Fatal(err)
	}
	defer removals.Close()
	const body = "synthetic repair preference"
	_, err = ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "source", ConversationID: "conversation", AgentID: "agent", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := memoryexec.NewMemory(db, nil, nil).Remember(t.Context(), memory.Write{Agent: "agent", Person: "person", Item: memory.Item{Kind: memory.Preference, Content: body, Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "source", Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	generation := t.TempDir()
	if err := os.Chmod(generation, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(generation, "canon"), 0700); err != nil {
		t.Fatal(err)
	}
	var dbPath string
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var sequence int
		var name string
		return tx.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &name, &dbPath)
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := canon.OpenSQLiteReader(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(generation, "canon/canon.sqlite")
	if err := canon.BackupSQLite(t.Context(), reader, path); err != nil {
		reader.Close()
		t.Fatal(err)
	}
	reader.Close()
	crash := errors.New("after sync")
	removals.SetCrash("after_sync", crash)
	err = removals.ForgetDuringIntent(t.Context(), "person", func(*canon.Tx) (retract.Intent, error) {
		return retract.Intent{Refs: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}, Repair: []byte(`{"v":1}`)}, nil
	})
	if !errors.Is(err, crash) {
		t.Fatalf("crash = %v", err)
	}
	journal, err := os.ReadFile(filepath.Join(source, "backups/removals"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(generation, "backups"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation, "backups/removals"), journal, 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareRestoredCanon(t.Context(), generation, 20, canon.Migrations(), nil); !errors.Is(err, retract.ErrRecovery) {
		t.Fatalf("missing repair = %v", err)
	}
	var got []byte
	if err := prepareRestoredCanon(t.Context(), generation, 20, canon.Migrations(), func(_ context.Context, _ *canon.Tx, raw []byte) error {
		got = append([]byte(nil), raw...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"v":1}` {
		t.Fatalf("replayed repair = %q", got)
	}
}
