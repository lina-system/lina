// SPDX-License-Identifier: Apache-2.0
package retract_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/retract"
)

// drainIndex publishes every queued source through the shared index, as Core's
// background drain does outside request paths.
func drainIndex(t *testing.T, db *canon.Store) {
	t.Helper()
	s := recall.New(db, nil)
	for range 1000 {
		var pending int
		if err := db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_pending").Scan(&pending)
		}); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			return
		}
		if _, err := s.DrainBatch(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("index queue did not drain")
}

// indexed returns the documents the shared index currently holds.
func indexed(t *testing.T, db *canon.Store) map[string]bool {
	t.Helper()
	docs := map[string]bool{}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(t.Context(), "SELECT kind,source_id FROM recall_doc")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, id string
			if err := rows.Scan(&kind, &id); err != nil {
				return err
			}
			docs[kind+":"+id] = true
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return docs
}

func requireIndex(t *testing.T, db *canon.Store, present []string, absent []string) {
	t.Helper()
	docs := indexed(t, db)
	for _, id := range present {
		if !docs[id] {
			t.Errorf("%s missing from the index", id)
		}
	}
	for _, id := range absent {
		if docs[id] {
			t.Errorf("%s still indexed", id)
		}
	}
}

func publishSummary(t *testing.T, db *canon.Store, conversation string, events ...string) {
	t.Helper()
	tree := compaction.New(db)
	frontier, err := tree.Frontier(t.Context(), conversation)
	if err != nil {
		t.Fatal(err)
	}
	cover := make([]compaction.Coverage, 0, len(events))
	for _, id := range events {
		cover = append(cover, compaction.Coverage{EventID: id})
	}
	if _, err := tree.Publish(t.Context(), compaction.Proposal{Expected: frontier, ID: "summary-" + conversation, Cover: cover,
		Output: compaction.Output{Model: "fixture", PromptVersion: "v1", BuiltAt: time.Unix(20, 0), Text: "도서관 요약", Dropped: "details"}}); err != nil {
		t.Fatal(err)
	}
}

func admit(t *testing.T, db *canon.Store, id, conversation, body string) {
	t.Helper()
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: conversation, PersonID: "person-1", AgentID: "agent-1",
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: body})
	if err != nil {
		t.Fatal(err)
	}
}

func eraseEvent(t *testing.T, db *canon.Store, id string) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO removal_applied(sequence,id,digest) VALUES(1,'erase-event','fixture')"); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), `INSERT INTO retract_target(kind,id,person,holder,agent,state,at,provenance,operation_id)
VALUES('event',?,'person-1','','agent-1','erased',50,'{}','erase-event')`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRemovalHidesCitingViewsFromTheSharedIndex(t *testing.T) {
	t.Run("forgotten item", func(t *testing.T) {
		db, removals, flow, _, _ := memoryFixture(t)
		source := seedMemory(t, db, flow, "source-1", "conversation-1", "도서관 violet source")
		unrelated := seedMemory(t, db, flow, "source-2", "conversation-2", "도서관 blue other")
		drainIndex(t, db)
		requireIndex(t, db, []string{"memory:" + source.ID, "memory:" + unrelated.ID, "event:source-1", "event:source-2"}, nil)
		if err := removals.Forget(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: source.ID}}}); err != nil {
			t.Fatal(err)
		}
		// The forgotten item and the event its evidence rests on leave the index.
		// Nothing else changes.
		requireIndex(t, db, []string{"memory:" + unrelated.ID, "event:source-2"}, []string{"memory:" + source.ID, "event:source-1"})
		if err := removals.Restore(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: source.ID}}}); err != nil {
			t.Fatal(err)
		}
		requireIndex(t, db, nil, []string{"memory:" + source.ID, "event:source-1"})
		drainIndex(t, db)
		requireIndex(t, db, []string{"memory:" + source.ID, "memory:" + unrelated.ID, "event:source-1", "event:source-2"}, nil)
	})

	t.Run("supersession chain", func(t *testing.T) {
		db, removals, flow, _, _ := memoryFixture(t)
		first := seedMemory(t, db, flow, "source-1", "conversation-1", "도서관 violet first")
		next := func(event, body, supersedes string) memory.Item {
			t.Helper()
			admit(t, db, event, "conversation-1", body)
			in := memoryWrite(event, body)
			in.Item.Supersedes = supersedes
			receipt, err := flow.Remember(t.Context(), in)
			if err != nil || !receipt.Remembered() {
				t.Fatalf("remember: %+v %v", receipt, err)
			}
			var item memory.Item
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT id FROM memory_item WHERE id=?", receipt.ID).Scan(&item.ID)
			}); err != nil {
				t.Fatal(err)
			}
			return item
		}
		second := next("source-2", "도서관 violet second", first.ID)
		third := next("source-3", "도서관 violet third", second.ID)
		unrelated := seedMemory(t, db, flow, "source-4", "conversation-2", "도서관 blue other")
		drainIndex(t, db)
		requireIndex(t, db, []string{"memory:" + third.ID, "memory:" + unrelated.ID}, nil)
		// Forgetting the replacement retracts every predecessor in one operation.
		if err := removals.Forget(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: third.ID}}}); err != nil {
			t.Fatal(err)
		}
		requireIndex(t, db, []string{"memory:" + unrelated.ID, "event:source-4"}, []string{"memory:" + first.ID, "memory:" + second.ID, "memory:" + third.ID})
	})

	t.Run("summary covers a retracted event", func(t *testing.T) {
		db, _, _, _, _ := memoryFixture(t)
		admit(t, db, "cover-1", "summary-conversation", "도서관 first")
		admit(t, db, "cover-2", "summary-conversation", "도서관 second")
		admit(t, db, "other-1", "other-conversation", "도서관 other")
		publishSummary(t, db, "summary-conversation", "cover-1", "cover-2")
		publishSummary(t, db, "other-conversation", "other-1")
		drainIndex(t, db)
		requireIndex(t, db, []string{"summary:summary-summary-conversation", "summary:summary-other-conversation", "event:cover-1"}, nil)
		eraseEvent(t, db, "cover-1")
		// The summary that covers the erased event is navigation derived from it,
		// so it leaves the index; the unrelated summary and event stay.
		requireIndex(t, db, []string{"summary:summary-other-conversation", "event:other-1"}, []string{"summary:summary-summary-conversation", "event:cover-1"})
		drainIndex(t, db)
		requireIndex(t, db, nil, []string{"summary:summary-summary-conversation", "event:cover-1"})
	})

	t.Run("item evidence is another forgotten item", func(t *testing.T) {
		db, removals, flow, _, _ := memoryFixture(t)
		root := seedMemory(t, db, flow, "source-1", "conversation-1", "도서관 violet root")
		child, err := deriveMemory(t, db, root.ID, "도서관 violet child")
		if err != nil {
			t.Fatal(err)
		}
		unrelated := seedMemory(t, db, flow, "source-2", "conversation-2", "도서관 blue other")
		drainIndex(t, db)
		requireIndex(t, db, []string{"memory:" + child.ID, "memory:" + unrelated.ID}, nil)
		request := retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: root.ID}}}
		if err := removals.Forget(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		requireIndex(t, db, []string{"memory:" + unrelated.ID}, []string{"memory:" + root.ID, "memory:" + child.ID})
		// Recovery re-queues what becomes visible again.
		if err := removals.Restore(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		requireIndex(t, db, nil, []string{"memory:" + root.ID, "memory:" + child.ID})
		drainIndex(t, db)
		requireIndex(t, db, []string{"memory:" + root.ID, "memory:" + child.ID, "memory:" + unrelated.ID}, nil)
	})

	t.Run("recovery keeps another hidden ancestor", func(t *testing.T) {
		db, removals, flow, _, _ := memoryFixture(t)
		first := seedMemory(t, db, flow, "source-1", "conversation-1", "도서관 violet first")
		second := seedMemory(t, db, flow, "source-2", "conversation-2", "도서관 blue second")
		child, err := deriveMemory(t, db, first.ID, "도서관 violet child")
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), `UPDATE memory_item SET record=json_insert(record,'$.evidence[#]',json_object('kind','item','id',?)) WHERE id=?`, second.ID, child.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		drainIndex(t, db)
		request := func(id string) retract.Request {
			return retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: id}}}
		}
		for _, id := range []string{first.ID, second.ID} {
			if err := removals.Forget(t.Context(), request(id)); err != nil {
				t.Fatal(err)
			}
		}
		requireIndex(t, db, nil, []string{"memory:" + first.ID, "memory:" + second.ID, "memory:" + child.ID})
		if err := removals.Restore(t.Context(), request(first.ID)); err != nil {
			t.Fatal(err)
		}
		// One restored ancestor is not enough: the other one still hides the child.
		requireIndex(t, db, nil, []string{"memory:" + first.ID, "memory:" + child.ID})
		if err := removals.Restore(t.Context(), request(second.ID)); err != nil {
			t.Fatal(err)
		}
		drainIndex(t, db)
		requireIndex(t, db, []string{"memory:" + first.ID, "memory:" + second.ID, "memory:" + child.ID}, nil)
	})

	t.Run("erased memory keeps its dependents hidden", func(t *testing.T) {
		db, removals, flow, _, _ := memoryFixture(t)
		root := seedMemory(t, db, flow, "source-1", "conversation-1", "도서관 violet root")
		child, err := deriveMemory(t, db, root.ID, "도서관 violet child")
		if err != nil {
			t.Fatal(err)
		}
		drainIndex(t, db)
		if err := removals.Delete(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: root.ID}}}); err != nil {
			t.Fatal(err)
		}
		requireIndex(t, db, nil, []string{"memory:" + root.ID, "memory:" + child.ID, "event:source-1"})
		// The erased ancestor is gone from memory_item; its tombstone still fences
		// a late publication of the dependent.
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), "INSERT OR IGNORE INTO recall_pending(kind,source_id) VALUES('memory',?)", child.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		drainIndex(t, db)
		requireIndex(t, db, nil, []string{"memory:" + child.ID})
	})
}

func TestForgetHidesOnlyItsOwnIndexDocuments(t *testing.T) {
	for _, count := range []int{100, 1000} {
		t.Run(fmt.Sprintf("items-%d", count), func(t *testing.T) {
			db, removals, flow, _, _ := memoryFixture(t)
			root := seedMemory(t, db, flow, "source-1", "conversation-1", "도서관 violet root")
			child, err := deriveMemory(t, db, root.ID, "도서관 violet child")
			if err != nil {
				t.Fatal(err)
			}
			seedUnrelatedSearchItems(t, db, count-2)
			drainIndex(t, db)
			// The root and its dependent are items; the event is the root's evidence.
			total := count + 1
			if got := len(indexed(t, db)); got != total {
				t.Fatalf("indexed %d, want %d", got, total)
			}
			countRecallWrites(t, db)
			request := retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: root.ID}}}
			if err := removals.Forget(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			// Only the documents the forget hides are deleted, and nothing is
			// queued for a hidden source.
			assertRecallWrites(t, db, 3, 0, nil)
			if got := len(indexed(t, db)); got != total-3 {
				t.Fatalf("indexed %d after forget, want %d", got, total-3)
			}
			if err := removals.Restore(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			// Recovery queues exactly what becomes visible again.
			assertRecallWrites(t, db, 0, 0, map[string]bool{"memory:" + root.ID: true, "memory:" + child.ID: true, "event:source-1": true})
			drainIndex(t, db)
			assertRecallWrites(t, db, 0, 3, nil)
			if got := len(indexed(t, db)); got != total {
				t.Fatalf("indexed %d after recovery, want %d", got, total)
			}
		})
	}
}

func countRecallWrites(t *testing.T, db *canon.Store) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TEMP TABLE recall_writes(operation TEXT, kind TEXT, id TEXT);
CREATE TEMP TRIGGER count_recall_doc_insert BEFORE INSERT ON main.recall_doc BEGIN
 INSERT INTO recall_writes VALUES('doc-insert',NEW.kind,NEW.source_id); END;
CREATE TEMP TRIGGER count_recall_doc_delete AFTER DELETE ON main.recall_doc BEGIN
 INSERT INTO recall_writes VALUES('doc-delete',OLD.kind,OLD.source_id); END;
CREATE TEMP TRIGGER count_recall_queue_insert BEFORE INSERT ON main.recall_pending BEGIN
 INSERT INTO recall_writes VALUES('queued',NEW.kind,NEW.source_id); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// assertRecallWrites checks the documents a change deleted and the set it
// queued. Repeated queue attempts for one document count once.
func assertRecallWrites(t *testing.T, db *canon.Store, deleted, inserted int, queued map[string]bool) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		var got int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_writes WHERE operation='doc-delete'").Scan(&got); err != nil {
			return err
		}
		if got != deleted {
			t.Errorf("deleted %d documents, want %d", got, deleted)
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_writes WHERE operation='doc-insert'").Scan(&got); err != nil {
			return err
		}
		if got != inserted {
			t.Errorf("index wrote %d documents, want %d", got, inserted)
		}
		want := queued
		if want == nil {
			want = map[string]bool{}
		}
		rows, err := tx.QueryContext(t.Context(), "SELECT DISTINCT kind,id FROM recall_writes WHERE operation='queued'")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, id string
			if err := rows.Scan(&kind, &id); err != nil {
				return err
			}
			if !want[kind+":"+id] {
				t.Errorf("queued %s:%s, which did not become visible", kind, id)
			}
			delete(want, kind+":"+id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for id := range want {
			t.Errorf("%s became visible but was not queued", id)
		}
		_, err = tx.ExecContext(t.Context(), "DELETE FROM recall_writes")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRestrictedEventsReadCitations(t *testing.T) {
	db, _, flow, _, _ := memoryFixture(t)
	protected := memoryWrite("source-1", "도서관 violet protected")
	protected.Item.Scope = memory.NeverLeaves
	admit(t, db, "source-1", "conversation-1", protected.Item.Content)
	receipt, err := flow.Remember(t.Context(), protected)
	if err != nil || !receipt.Remembered() {
		t.Fatalf("remember: %+v %v", receipt, err)
	}
	// The protected event id now comes from cite, not from the record JSON.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE memory_item SET record=json_remove(record,'$.evidence') WHERE id=?`, receipt.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := memory.RecallScope{Agent: "agent-1", Person: "person-1", ExternalModel: true}
	ids, err := memory.New(db).RestrictedEvents(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if !ids["source-1"] {
		t.Fatalf("citation scope check missing: %v", ids)
	}
}
