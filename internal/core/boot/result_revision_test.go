// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/result"
)

func TestPlanAndCompletionHistoryKeepCurrentCards(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "mixed-result-history"
	var err error
	r.records, err = records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.records.Close() })
	if err := r.initPlanning(); err != nil {
		t.Fatal(err)
	}
	store, err := r.resultStore(ledger.Surface{Kind: "tui"})
	if err != nil {
		t.Fatal(err)
	}
	scope := r.planScope("mixed-turn")
	claim, err := store.Present(t.Context(), scope, result.PresentRequest{Type: result.DraftType, Title: "Result", Summary: "Complete.", Body: json.RawMessage(`{"blocks":[{"kind":"paragraph","text":"Content."}],"from_summary":null}`), Completion: &result.CompletionRequest{WorkKind: "document"}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := r.plans.Accept(t.Context(), scope, "goal", "Deliver workshop", "Workshop delivered")
	if err != nil {
		t.Fatal(err)
	}
	input, err := r.plans.Input(t.Context(), scope, root.ID, "", 0, "Prepare a plan")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(jobapi.PlanDraft{Title: "Workshop plan", Summary: "Prepare workshop", Entries: []jobapi.PlanEntry{{ID: "project", Kind: "project", Title: "Workshop", Done: "Ready"}}})
	if err != nil {
		t.Fatal(err)
	}
	var plan result.Card
	for range 2 {
		if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
			var err error
			plan, err = r.plans.DraftTx(t.Context(), tx, scope, input, raw)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		input.ResultID, input.BaseRevision = plan.ID, plan.Revision
	}
	if err := r.db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(r.root, "canon.sqlite"), Epoch: r.epoch + 1, Controls: r.controls, Migrations: ApplicationMigrations(), NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	restarted := &conversationRuntime{db: db, conversation: r.conversation}
	for range 2 {
		items, err := restarted.historyItems(t.Context())
		if err != nil || len(items) != 2 {
			t.Fatalf("mixed history lost or duplicated cards: %+v %v", items, err)
		}
		seen := map[string]bool{}
		for _, item := range items {
			if seen[item.key] {
				t.Fatalf("duplicate result key: %s", item.key)
			}
			seen[item.key] = true
			event, err := restarted.journal().Get(t.Context(), item.eventID)
			if err != nil {
				t.Fatal(err)
			}
			var value result.Result
			if err := json.Unmarshal([]byte(event.Body.Text()), &value); err != nil {
				t.Fatal(err)
			}
			switch item.key {
			case plan.ID:
				if value.ID != plan.ID || value.Revision != 2 || value.Type != result.PlanType {
					t.Fatalf("wrong plan revision: %+v", value)
				}
			case "result-card:" + claim.ID:
				if value.ID != claim.ID || value.Completion == nil || value.Completion.State != "claimed" {
					t.Fatalf("completion assessment lost: %+v", value)
				}
			default:
				t.Fatalf("unexpected result key: %s", item.key)
			}
		}
	}
}

func TestResultRevisionReplacesUnverifiedCard(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "result-history"
	var err error
	r.topics, err = clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.topics.Close)
	store, err := r.resultStore(ledger.Surface{Kind: "tui"})
	if err != nil {
		t.Fatal(err)
	}
	scope := result.Scope{PersonID: r.person, AgentID: agent, ConversationID: r.conversation, TurnID: "turn"}
	p := result.PresentRequest{Type: result.DraftType, Title: "Result", Summary: "Complete.", Body: json.RawMessage(`{"blocks":[{"kind":"paragraph","text":"Content."}],"from_summary":null}`), Completion: &result.CompletionRequest{WorkKind: "document"}}
	first, err := store.Present(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Read(t.Context(), scope, result.ReadRequest{ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.publishResult(t.Context(), value); err != nil {
		t.Fatal(err)
	}
	p.ID, p.BaseRevision = first.ID, first.Revision
	p.Completion.Evidence = []result.EvidenceReference{{Kind: "source_reopened", TurnID: "turn", CallID: "read"}}
	ctx := result.WithCompletionPolicy(t.Context(), func(req result.CompletionRequest) result.Completion {
		return result.AssessCompletion(req, nil, func(result.EvidenceReference) bool { return true })
	})
	if _, err = store.Present(ctx, scope, p); err != nil {
		t.Fatal(err)
	}
	value, err = store.Read(t.Context(), scope, result.ReadRequest{ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.publishResult(t.Context(), value); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		items, err := r.historyItems(t.Context())
		if err != nil || len(items) != 1 || items[0].key != "result-card:"+first.ID {
			t.Fatalf("stale completion remains in history: %+v %v", items, err)
		}
		event, err := r.journal().Get(t.Context(), items[0].eventID)
		if err != nil {
			t.Fatal(err)
		}
		var current result.Result
		if err = json.Unmarshal([]byte(event.Body.Text()), &current); err != nil || current.Revision != 2 || current.Completion.State != "done" {
			t.Fatalf("wrong restored card: %+v %v", current, err)
		}
	}
	var versions int
	err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE conversation_id=? AND scaffold_kind='result'", r.conversation).Scan(&versions)
	})
	if err != nil || versions != 2 {
		t.Fatalf("revision history lost: %d %v", versions, err)
	}
}
