// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

func TestLedgerMemoryChecksRealAdmissionProvenance(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	flow := NewMemory(db, nil, nil)
	for i, entry := range []guard.Entry{guard.EntryOwner, guard.EntryAgent, guard.EntryExternalParticipant, guard.EntryAutomation, guard.EntryForwarded, guard.EntrySystem, guard.EntryTool} {
		id := fmt.Sprintf("event-%d", i)
		in := ledger.Input{ID: id, ConversationID: "conversation", AgentID: "A", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "person: I like tea", Locator: "synthetic-source"}
		if entry == guard.EntryOwner {
			in.PersonID = "person"
		}
		if _, err := events.Admit(t.Context(), entry, in); err != nil {
			t.Fatal(err)
		}
		write := rememberInput("likes tea")
		write.Item.Evidence[0].ID = id
		receipt, err := flow.Remember(t.Context(), write)
		if entry == guard.EntryOwner {
			if err != nil || !receipt.Remembered() {
				t.Fatalf("owner refused: %v", err)
			}
		} else if !errors.Is(err, memory.ErrPersonEvidence) || receipt.Remembered() {
			t.Fatalf("entry %v impersonated person: %+v %v", entry, receipt, err)
		}
	}
	write := rememberInput("not admitted")
	write.Item.Evidence[0].ID = "missing"
	if receipt, err := flow.Remember(t.Context(), write); err == nil || receipt.Remembered() {
		t.Fatal("missing event accepted")
	}
	hits, err := flow.store.Recall(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, memory.Query{At: 100})
	if err != nil || len(hits) != 1 || hits[0].Evidence[0].Quote != "person: I like tea" {
		t.Fatalf("canonical evidence: %+v %v", hits, err)
	}
}
func TestRecallPersonReadsOwnLedgerAndExcludesSecretSpans(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	flow := NewMemory(db, nil, nil)
	for _, agent := range []string{"A", "B"} {
		_, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: agent, ConversationID: agent, AgentID: agent, PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "I like tea, api_key=synthetic-secret-value"})
		if err != nil {
			t.Fatal(err)
		}
	}
	scope := memory.RecallScope{Agent: "A", Person: "person"}
	answer, err := flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "What drink?", Query: "tea", At: 100})
	if err != nil || len(answer.Sources) != 1 || answer.Sources[0].ID != "event:A" {
		t.Fatalf("ledger scope: %+v %v", answer, err)
	}
	if strings.Contains(answer.Sources[0].Text, "synthetic-secret-value") || strings.Contains(answer.Sources[0].Text, "[secret]") {
		t.Fatal("secret span entered recall")
	}
	scope.Agent = "C"
	answer, err = flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "What drink?", Query: "tea", At: 100})
	if err != nil || len(answer.Sources) != 0 {
		t.Fatalf("foreign ledger read: %+v %v", answer, err)
	}
}

type sourceMaterials struct {
	bodies map[ledger.Material]string
	reads  int
}

func (s *sourceMaterials) Prepare(_ context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	ref := ledger.Material{ID: fmt.Sprintf("source-%d", len(s.bodies)), Revision: 1}
	return sourceMaterialWrite{store: s, ref: ref, body: body.Text()}, nil
}

type sourceMaterialWrite struct {
	store *sourceMaterials
	ref   ledger.Material
	body  string
}

func (w sourceMaterialWrite) Insert(context.Context, *canon.Tx) (ledger.Material, error) {
	w.store.bodies[w.ref] = w.body
	return w.ref, nil
}
func (w sourceMaterialWrite) Close() error { return nil }
func (s *sourceMaterials) Read(_ context.Context, ref ledger.Material) (string, error) {
	s.reads++
	body, ok := s.bodies[ref]
	if !ok {
		return "", ledger.ErrBytesUnavailable
	}
	return body, nil
}
func TestRememberLargeSourceAndReplayKeepsOneLineage(t *testing.T) {
	db := memoryDB(t)
	materials := &sourceMaterials{bodies: map[ledger.Material]string{}}
	events := ledger.New(db, materials)
	flow := NewMemory(db, materials, nil)
	input := ledger.Input{ID: "original", ConversationID: "conversation", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: strings.Repeat("likes tea ", 2000)}
	if _, err := events.Admit(t.Context(), guard.EntryOwner, input); err != nil {
		t.Fatal(err)
	}
	write := rememberInput("likes tea")
	write.Item.Evidence[0].ID = input.ID
	first, err := flow.Remember(t.Context(), write)
	if err != nil {
		t.Fatal(err)
	}
	if materials.reads != 1 {
		t.Fatalf("material was not prefetched once: %d", materials.reads)
	}
	input.ID = "replay"
	input.ReplayOf = "original"
	input.Body = "likes tea"
	if _, err := events.Admit(t.Context(), guard.EntryOwner, input); err != nil {
		t.Fatal(err)
	}
	write.Item.Evidence[0].ID = input.ID
	second, err := flow.Remember(t.Context(), write)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Revision != first.Revision {
		t.Fatalf("replay became independent evidence: %+v %+v", first, second)
	}
	hits, err := flow.store.Recall(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, memory.Query{At: 100})
	if err != nil || len(hits) != 1 || len(hits[0].Evidence) != 1 || hits[0].Evidence[0].ID != "original" {
		t.Fatalf("lineage: %+v %v", hits, err)
	}
}

func TestRecallPersonCountsRememberedEventOnce(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	flow := NewMemory(db, nil, nil)
	_, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "event", ConversationID: "conversation", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "likes tea"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Remember(t.Context(), rememberInput("likes tea")); err != nil {
		t.Fatal(err)
	}
	answer, err := flow.RecallPerson(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, PersonRecall{Question: "What drink?", Query: "tea", At: 100})
	if err != nil || len(answer.Sources) != 1 {
		t.Fatalf("source counted through memory and ledger: %+v %v", answer, err)
	}
}
