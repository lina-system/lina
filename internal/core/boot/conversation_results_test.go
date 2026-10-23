// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/results"
	"github.com/thisisjun786/lina/kit/result"
)

func TestTypedResultAndBreachCommitTogether(t *testing.T) {
	stored, _, _, _ := managementFixture(t)
	shapes := reply.Shapes{Status: "verdict-first", Comparison: "verdict-then-card", Candidates: 1, ThirdParty: "recipient-only"}
	journal := measuredResultJournal{LedgerJournal: results.LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, config: conversationReplyConfig(&shapes)}
	store, err := results.New(stored.db, results.Config{Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	scope := result.Scope{PersonID: "owner", AgentID: "a", ConversationID: "conversation", TurnID: "turn"}
	p := result.PresentRequest{ReplyType: "candidates", Type: result.TableType, Title: "Choices", Summary: "Choose A.", Body: json.RawMessage(`{"columns":[{"name":"Choice","type":"text","unit":""}],"rows":[["A"],["B"]]}`)}
	if err := stored.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER fail_reply_check BEFORE INSERT ON conv_event WHEN NEW.scaffold_kind='reply_check' BEGIN SELECT RAISE(ABORT,'injected measurement failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Present(t.Context(), scope, p); err == nil {
		t.Fatal("failed measurement acknowledged")
	}
	if err := stored.db.Read(t.Context(), func(tx *canon.Tx) error {
		var count int
		if err := tx.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM result_heads)+(SELECT count(*) FROM conv_event WHERE conversation_id='conversation')`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("partial result/check survived rollback: %d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := stored.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `DROP TRIGGER fail_reply_check`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	card, err := store.Present(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	for agent, want := range map[string]int{"a": 1, "b": 0} {
		counts, err := reply.Breaches(t.Context(), stored.db, agent)
		if err != nil || counts.Candidates != want || counts.Replies != want {
			t.Fatalf("%s counts=%+v err=%v", agent, counts, err)
		}
	}
	// Read and failed stale writes are not new authored result revisions.
	if _, err := store.Read(t.Context(), scope, result.ReadRequest{ID: card.ID}); err != nil {
		t.Fatal(err)
	}
	p.ID, p.BaseRevision = card.ID, 2
	if _, err := store.Present(t.Context(), scope, p); err == nil {
		t.Fatal("stale revision accepted")
	}
	counts, err := reply.Breaches(t.Context(), stored.db, "a")
	if err != nil || counts.Replies != 1 || counts.Candidates != 1 {
		t.Fatalf("retry/read recounted: %+v %v", counts, err)
	}
}
