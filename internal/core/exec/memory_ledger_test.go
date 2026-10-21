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
	hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, memory.Query{At: 100})
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
