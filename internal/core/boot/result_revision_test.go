// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/result"
)

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
