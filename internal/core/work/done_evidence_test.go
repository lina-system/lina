// SPDX-License-Identifier: Apache-2.0
package work

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/result"
	"testing"
)

func TestTaskCompletionWithoutDoneEvidenceIsClaimed(t *testing.T) {
	s, _ := testStore(t)
	startFixture(t, s)
	r := receiptFixture()
	if err := s.Submit(t.Context(), "run-1", r); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(t.Context(), "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := s.Judge(t.Context(), "task-1", r.EventID, "verified", "manager checked the artifact"); err != nil {
		t.Fatal(err)
	}
	card, err := s.Get(t.Context(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(card)
	var got struct{ Completion struct{ State, Label string } }
	_ = json.Unmarshal(raw, &got)
	if got.Completion.State != "claimed" || got.Completion.Label != "not verified" {
		t.Fatalf("missing work-kind evidence must remain claimed: %s", raw)
	}
}

func TestTaskCompletionEvidenceSurvivesReplay(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "observed"}[verified], func(t *testing.T) {
			s, db := testStore(t)
			task := taskFixture()
			task.WorkKind = "document"
			if err := s.Create(t.Context(), task); err != nil {
				t.Fatal(err)
			}
			if err := s.Start(t.Context(), runFixture()); err != nil {
				t.Fatal(err)
			}
			receipt := receiptFixture()
			if err := s.Submit(t.Context(), "run-1", receipt); err != nil {
				t.Fatal(err)
			}
			if err := s.Finish(t.Context(), task.ID, "run-1", "completed"); err != nil {
				t.Fatal(err)
			}
			proof := result.CompletionRequest{WorkKind: "peer_report"}
			if verified {
				proof.Evidence = []result.EvidenceReference{{Kind: "source_reopened", TurnID: "turn", CallID: "read"}}
			}
			ctx := result.WithCompletionPolicy(t.Context(), func(req result.CompletionRequest) result.Completion {
				return result.AssessCompletion(req, nil, func(result.EvidenceReference) bool { return true })
			})
			if err := s.Judge(ctx, task.ID, receipt.EventID, "verified", "checked", proof); err != nil {
				t.Fatal(err)
			}
			// A restarted service and a later policy must reuse the first decision.
			s = New(db)
			if err := s.Rebuild(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := s.Judge(t.Context(), task.ID, receipt.EventID, "verified", "checked", proof); err != nil {
				t.Fatal(err)
			}
			card, err := s.Get(t.Context(), task.ID)
			want := "claimed"
			if verified {
				want = "done"
			}
			if err != nil || card.Completion == nil || card.Completion.State != want || card.Completion.WorkKind != "document" {
				t.Fatalf("replayed completion: %+v %v", card, err)
			}
			var count int
			err = db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM done_completions WHERE agent_id=? AND state='claimed'", task.AgentID).Scan(&count)
			})
			wantCount := 1
			if verified {
				wantCount = 0
			}
			if err != nil || count != wantCount {
				t.Fatalf("breach replay: %d %v", count, err)
			}
		})
	}
}
