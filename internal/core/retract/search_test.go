// SPDX-License-Identifier: Apache-2.0
package retract_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func TestForgetPurgesKoreanIndexesAndRestoreDoesNotResurrectThem(t *testing.T) {
	db, s, flow, root, close := memoryFixture(t)
	receipt := seedMemory(t, db, flow, "source-1", "conversation-1", "도서관에서 커피를 마신다")
	scope := memory.RecallScope{Agent: "agent-1", Person: "person-1"}
	search := func(want int) {
		t.Helper()
		for _, query := range []string{"도서관", "커피"} {
			events, err := ledger.New(db, nil).Search(t.Context(), ledger.SearchQuery{AgentID: "agent-1", PersonID: "person-1", Text: query})
			if err != nil || len(events) != want {
				t.Fatalf("ledger %q: %+v %v", query, events, err)
			}
			items, err := memory.New(db).Recall(t.Context(), scope, memory.Query{Text: query, At: 100})
			if err != nil || len(items) != want {
				t.Fatalf("memory %q: %+v %v", query, items, err)
			}
		}
	}
	search(1)
	generation := filepath.Join(t.TempDir(), "canon.sqlite")
	snapshotCanon(t, filepath.Join(root, "canon.sqlite"), generation)
	r := retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}
	if err := s.Forget(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	assertNoSearchRows(t, db)
	search(0)
	// Simulate a backfill that read its body before forgetting and commits late.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO ledger_search_text(event_id,body) VALUES('source-1','도서관에서 커피를 마신다');
INSERT INTO ledger_search_bigram(gram,event_id) VALUES('커피','source-1');
INSERT OR IGNORE INTO ledger_search_pending VALUES('source-1');`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), `INSERT INTO memory_search_text(sequence,item_id,body) SELECT sequence,id,normalized FROM memory_item;
INSERT INTO memory_search_bigram(gram,item_id) SELECT '커피',id FROM memory_item;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertNoSearchRows(t, db)
	if err := s.Restore(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	search(1)
	if err := s.Delete(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	assertNoSearchRows(t, db)
	removals, err := os.ReadFile(filepath.Join(root, "backups", "removals"))
	if err != nil {
		t.Fatal(err)
	}
	close()
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "backups"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backups", "removals"), removals, 0600); err != nil {
		t.Fatal(err)
	}
	snapshotCanon(t, generation, filepath.Join(root, "canon.sqlite"))
	db, s, flow, _, _ = memoryAt(t, root)
	assertNoSearchRows(t, db)
	search(0)
}

func assertNoSearchRows(t *testing.T, db *canon.Store) {
	t.Helper()
	if err := db.Read(context.Background(), func(tx *canon.Tx) error {
		for _, table := range []string{"ledger_search_text", "ledger_search_bigram", "ledger_search_pending", "memory_search_text", "memory_search_bigram"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatalf("forgotten data retained in %s: %d", table, n)
			}
		}
		for _, query := range []string{"SELECT count(*) FROM ledger_search WHERE ledger_search MATCH '도서관'", "SELECT count(*) FROM memory_search WHERE memory_search MATCH '도서관'"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), query).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatal("FTS retained forgotten posting")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestForgetExcludesExistingAndFutureLedgerCopies(t *testing.T) {
	db, s, flow, root, close := memoryFixture(t)
	receipt := seedMemory(t, db, flow, "source-1", "conversation-1", forgotten)
	events := ledger.New(db, nil)
	copyEvent := func(id, source string, replay bool) {
		t.Helper()
		in := ledger.Input{ID: id, ConversationID: "conversation-1", AgentID: "agent-1", PersonID: "person-1", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(11, 0), RecordedAt: time.Unix(11, 0), TimeQuality: "observed", Body: forgotten}
		if replay {
			in.ReplayOf = source
		} else {
			in.Relations = []ledger.Relation{{Kind: "view_of", TargetID: source}}
		}
		if _, err := events.Admit(t.Context(), guard.EntryOwner, in); err != nil {
			t.Fatal(err)
		}
	}
	copyEvent("view-1", "source-1", false)
	copyEvent("replay-1", "view-1", true)
	copyEvent("nested-view", "replay-1", false)
	if hits, err := events.Search(t.Context(), ledger.SearchQuery{AgentID: "agent-1", PersonID: "person-1", Text: "violet", IncludeReplay: true}); err != nil || len(hits) != 4 {
		t.Fatalf("copy precondition: %+v %v", hits, err)
	}
	generation := filepath.Join(t.TempDir(), "canon.sqlite")
	snapshotCanon(t, filepath.Join(root, "canon.sqlite"), generation)
	if err := s.Forget(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}); err != nil {
		t.Fatal(err)
	}
	copyEvent("late-view", "nested-view", false)
	copyEvent("late-replay", "late-view", true)
	for _, includeReplay := range []bool{false, true} {
		hits, err := events.Search(t.Context(), ledger.SearchQuery{AgentID: "agent-1", PersonID: "person-1", Text: "violet", IncludeReplay: includeReplay})
		if err != nil || len(hits) != 0 {
			t.Fatalf("copy search leaked: %+v %v", hits, err)
		}
	}
	view, err := s.Snapshot(t.Context(), "person-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"source-1", "view-1", "replay-1", "nested-view", "late-view", "late-replay"} {
		if event, err := events.Get(t.Context(), id); err != nil || event.Body.Text() != forgotten {
			t.Fatalf("copy missing from conversation display: %s %v", id, err)
		}
		if view.Visible(retract.Ref{Kind: retract.Event, ID: id}) {
			t.Fatalf("copy in preparation: %s", id)
		}
	}
	assertNoSearchRows(t, db)
	if err := s.Restore(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}); err != nil {
		t.Fatal(err)
	}
	if hits, err := events.Search(t.Context(), ledger.SearchQuery{AgentID: "agent-1", PersonID: "person-1", Text: "violet", IncludeReplay: true}); err != nil || len(hits) != 6 {
		t.Fatalf("recovered lineage: %+v %v", hits, err)
	}
	if err := s.Delete(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}); err != nil {
		t.Fatal(err)
	}
	removals, err := os.ReadFile(filepath.Join(root, "backups", "removals"))
	if err != nil {
		t.Fatal(err)
	}
	close()
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "backups"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backups", "removals"), removals, 0600); err != nil {
		t.Fatal(err)
	}
	snapshotCanon(t, generation, filepath.Join(root, "canon.sqlite"))
	db, s, flow, _, _ = memoryAt(t, root)
	events = ledger.New(db, nil)
	if hits, err := events.Search(t.Context(), ledger.SearchQuery{AgentID: "agent-1", PersonID: "person-1", Text: "violet", IncludeReplay: true}); err != nil || len(hits) != 0 {
		t.Fatalf("restored copies exposed: %+v %v", hits, err)
	}
	assertNoSearchRows(t, db)
}
