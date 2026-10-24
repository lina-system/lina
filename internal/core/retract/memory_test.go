// SPDX-License-Identifier: Apache-2.0
package retract_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func memoryFixture(t *testing.T) (*canon.Store, *retract.Store, *memoryexec.Memory, string, func()) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	return memoryAt(t, root)
}

func memoryAt(t *testing.T, root string) (*canon.Store, *retract.Store, *memoryexec.Memory, string, func()) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	close := func() { once.Do(func() { s.Close(); db.Close(context.Background()); controls.Close() }) }
	t.Cleanup(close)
	flow := memoryexec.NewMemory(db, nil, func(_ context.Context, r memoryexec.RecallRequest) ([]memoryexec.Sentence, error) {
		var answer []memoryexec.Sentence
		for _, source := range r.Sources {
			answer = append(answer, memoryexec.Sentence{Text: source.Text, Citations: []memoryexec.Citation{source.Citation}})
		}
		return answer, nil
	})
	return db, s, flow, root, close
}

const forgotten = "synthetic violet preference"

func seedMemory(t *testing.T, db *canon.Store, flow *memoryexec.Memory, event, conversation, content string) memoryexec.RememberReceipt {
	t.Helper()
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: event, ConversationID: conversation, PersonID: "person-1", AgentID: "agent-1", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: content})
	if err != nil {
		t.Fatal(err)
	}
	r, err := flow.Remember(t.Context(), memoryWrite(event, content))
	if err != nil || !r.Remembered() {
		t.Fatalf("remember: %+v %v", r, err)
	}
	return r
}
func memoryWrite(event, content string) memory.Write {
	return memory.Write{Agent: "agent-1", Person: "person-1", Item: memory.Item{Kind: memory.Preference, Content: content, Subject: memory.Subject{Kind: memory.Person, ID: "person-1"}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: event, Precision: memory.Exact}}}}
}

func TestForgetHidesMemoryAndLedgerFallbackAndBlocksExtraction(t *testing.T) {
	t.Parallel()
	db, s, flow, _, _ := memoryFixture(t)
	r := seedMemory(t, db, flow, "source-1", "conversation-1", forgotten)
	scope := memory.RecallScope{Agent: "agent-1", Person: "person-1"}
	before, err := flow.RecallPerson(t.Context(), scope, memoryexec.PersonRecall{Question: "What preference?", Query: "violet", At: 100, Depth: 1})
	if err != nil || len(before.Sources) != 1 || len(before.Answer) != 1 {
		t.Fatalf("initial recall: %+v %v", before, err)
	}
	if err := s.Forget(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: r.ID}}}); err != nil {
		t.Fatal(err)
	}
	hits, err := memory.New(db).Recall(t.Context(), scope, memory.Query{Text: "violet", At: 100})
	if err != nil || len(hits) != 0 {
		t.Fatalf("memory leak: %+v %v", hits, err)
	}
	profile, err := memory.New(db).Profile(t.Context(), scope, 100, 10, 100, func(string) int { return 1 })
	if err != nil || len(profile) != 0 {
		t.Fatalf("profile leak: %+v %v", profile, err)
	}
	answer, err := flow.RecallPerson(t.Context(), scope, memoryexec.PersonRecall{Question: "What preference?", Query: "violet", At: 100, Depth: 1})
	if err != nil || len(answer.Sources) != 0 || len(answer.Answer) != 0 {
		t.Fatalf("ledger fallback resurrected fact: %+v %v", answer, err)
	}
	if receipt, err := flow.Remember(t.Context(), memoryWrite("source-1", forgotten)); err == nil || receipt.Remembered() {
		t.Fatal("forgotten evidence re-extracted")
	}
	// A different event in the same conversation cannot recreate the item either.
	_, err = ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "source-2", ConversationID: "conversation-1", PersonID: "person-1", AgentID: "agent-1", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(11, 0), RecordedAt: time.Unix(11, 0), TimeQuality: "observed", Body: forgotten})
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := flow.Remember(t.Context(), memoryWrite("source-2", forgotten)); err == nil || receipt.Remembered() {
		t.Fatal("same-conversation extraction recreated forgotten item")
	}
}

func TestPermanentDeletionErasesEveryMemoryRevisionButKeepsProvenance(t *testing.T) {
	t.Parallel()
	db, s, flow, root, _ := memoryFixture(t)
	r := seedMemory(t, db, flow, "source-1", "conversation-1", forgotten)
	seedMemory(t, db, flow, "source-2", "conversation-2", forgotten) // same item, next evidence revision
	if err := s.Delete(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: r.ID}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, query := range []string{"SELECT count(*) FROM memory_item", "SELECT count(*) FROM memory_revision"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), query).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatal("deleted memory revisions retained")
			}
		}
		var state string
		var data []byte
		if err := tx.QueryRowContext(t.Context(), "SELECT state,provenance FROM retract_target WHERE id=?", r.ID).Scan(&state, &data); err != nil {
			return err
		}
		if state != "erased" || strings.Contains(string(data), forgotten) || !strings.Contains(string(data), "source-1") || !strings.Contains(string(data), "source-2") {
			t.Fatalf("tombstone: %s %s", state, data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "backups", "removals"))
	if err != nil || strings.Contains(string(data), forgotten) {
		t.Fatalf("content in removal list: %s %v", data, err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		var body string
		if err := tx.QueryRowContext(t.Context(), "SELECT body FROM conv_event WHERE id='source-1'").Scan(&body); err != nil {
			return err
		}
		if body != forgotten {
			t.Fatal("forget rewrote the canonical conversation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverOneItemKeepsOtherExtractionExclusions(t *testing.T) {
	t.Parallel()
	db, s, flow, _, _ := memoryFixture(t)
	first := seedMemory(t, db, flow, "source-1", "conversation-1", forgotten)
	second, err := flow.Remember(t.Context(), memoryWrite("source-1", "synthetic amber preference"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: first.ID}, {Kind: retract.Memory, ID: second.ID}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: first.ID}}}); err != nil {
		t.Fatal(err)
	}
	hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Agent: "agent-1", Person: "person-1"}, memory.Query{At: 100})
	if err != nil || len(hits) != 1 || hits[0].ID != first.ID || hits[0].State != memory.Active {
		t.Fatalf("recovered memory: %+v %v", hits, err)
	}
	if receipt, err := flow.Remember(t.Context(), memoryWrite("source-1", "synthetic amber preference")); err == nil || receipt.Remembered() {
		t.Fatal("one recovery cleared another item's extraction exclusion")
	}
}

func TestProtectedEvidenceCannotLeaveThroughAnotherHolder(t *testing.T) {
	t.Parallel()
	db, _, flow, _, _ := memoryFixture(t)
	seedMemory(t, db, flow, "source-1", "conversation-1", forgotten)
	protected := memoryWrite("source-1", "synthetic private restriction")
	protected.Item.Holder = "agent-1"
	protected.Item.Scope = memory.NeverLeaves
	protected.ExplicitHolder = true
	if _, err := flow.Remember(t.Context(), protected); err != nil {
		t.Fatal(err)
	}
	scope := memory.RecallScope{Agent: "agent-2", Person: "person-1"}
	local, err := memory.New(db).Recall(t.Context(), scope, memory.Query{Text: "violet", At: 100})
	if err != nil || len(local) != 1 {
		t.Fatalf("common-memory precondition: %+v %v", local, err)
	}
	remote, err := flow.RecallPerson(t.Context(), scope, memoryexec.PersonRecall{Question: "What preference?", Query: "violet", At: 100, Depth: 1})
	if err != nil || len(remote.Sources) != 0 || len(remote.Answer) != 0 {
		t.Fatalf("protected source crossed holders: %+v %v", remote, err)
	}
}

func TestMemoryForgetPreservesAuthorizedConversationDisplay(t *testing.T) {
	t.Parallel()
	db, s, flow, _, _ := memoryFixture(t)
	receipt := seedMemory(t, db, flow, "source-1", "conversation-1", forgotten)
	if err := s.Forget(t.Context(), retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}); err != nil {
		t.Fatal(err)
	}
	event, err := ledger.New(db, nil).Get(t.Context(), "source-1")
	if err != nil || event.Body.Text() != forgotten {
		t.Fatalf("memory forget removed conversation display: %+v %v", event, err)
	}
	view, err := s.Snapshot(t.Context(), "person-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Visible(retract.Ref{Kind: retract.Event, ID: event.ID}) {
		t.Fatal("displayable conversation event became model evidence")
	}
}

func deriveMemory(t *testing.T, db *canon.Store, parent, text string) (memory.Item, error) {
	t.Helper()
	var item memory.Item
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		in := memory.Write{Agent: "agent-1", Person: "person-1", Item: memory.Item{Kind: memory.Fact, Content: text, Subject: memory.Subject{Kind: memory.World}, Basis: memory.Derived, Evidence: []memory.Evidence{{Kind: memory.MemoryItem, ID: parent, Revision: 1, Precision: memory.Exact}}}}
		var err error
		item, err = memory.Insert(t.Context(), tx, in, func(ctx context.Context, tx *canon.Tx, ref memory.Evidence) (memory.Origin, error) {
			var raw []byte
			var source memory.Item
			if err := tx.QueryRowContext(ctx, "SELECT record FROM memory_item WHERE id=?", ref.ID).Scan(&raw); err != nil {
				return memory.Origin{}, err
			}
			if err := json.Unmarshal(raw, &source); err != nil {
				return memory.Origin{}, err
			}
			return memory.Origin{Person: source.Person, Agent: source.SourceAgent, Scope: source.Scope, Quote: source.Content}, nil
		})
		return err
	})
	return item, err
}

func TestForgetExcludesTransitiveDerivedMemory(t *testing.T) {
	t.Parallel()
	db, s, flow, root, close := memoryFixture(t)
	source := seedMemory(t, db, flow, "source-1", "conversation-1", forgotten)
	first, err := deriveMemory(t, db, source.ID, forgotten+" direct")
	if err != nil {
		t.Fatal(err)
	}
	second, err := deriveMemory(t, db, first.ID, forgotten+" transitive")
	if err != nil {
		t.Fatal(err)
	}
	scope := memory.RecallScope{Agent: "agent-1", Person: "person-1"}
	check := func(want int) {
		t.Helper()
		hits, err := memory.New(db).Recall(t.Context(), scope, memory.Query{Text: "violet", At: 100})
		if err != nil || len(hits) != want {
			t.Fatalf("derived recall: %+v %v", hits, err)
		}
	}
	check(3)
	generation := filepath.Join(t.TempDir(), "canon.sqlite")
	snapshotCanon(t, filepath.Join(root, "canon.sqlite"), generation)
	r := retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: source.ID}}}
	if err := s.Forget(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	check(0)
	view, err := s.Snapshot(t.Context(), "person-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{source.ID, first.ID, second.ID} {
		if view.Visible(retract.Ref{Kind: retract.Memory, ID: id}) {
			t.Fatalf("derived packet input survived: %s", id)
		}
	}
	assertNoSearchRows(t, db)
	if _, err := deriveMemory(t, db, second.ID, "another derived violet fact"); err == nil {
		t.Fatal("new derivation from excluded ancestor committed")
	}
	if err := s.Restore(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	check(3)
	if err := s.Delete(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	check(0)
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
	db, _, _, _, _ = memoryAt(t, root)
	check(0)
	assertNoSearchRows(t, db)
}
