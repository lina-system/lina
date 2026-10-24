// SPDX-License-Identifier: Apache-2.0
package work

import (
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/result"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitApplicationRetainsAcceptedCompletion(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"observed", "missing", "wrong-kind", "extended-policy", "moved-head"} {
		t.Run(mode, func(t *testing.T) {
			s, db := testStore(t)
			task := taskFixture()
			task.WorkKind, task.RequiresApply = "code", true
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(s.Create(t.Context(), task))
			must(s.Start(t.Context(), runFixture()))
			receipt := receiptFixture()
			must(s.Submit(t.Context(), "run-1", receipt))
			must(s.Finish(t.Context(), task.ID, "run-1", "completed"))
			must(s.ObserveHead(t.Context(), task.ID, "run-1", strings.Repeat("a", 40)))
			proof := result.CompletionRequest{WorkKind: "code"}
			if mode != "missing" {
				proof.Evidence = []result.EvidenceReference{{Kind: "owner_path_run", TurnID: "manager-turn", CallID: "check"}}
			}
			if mode == "wrong-kind" {
				proof.Evidence[0].Kind = "labelled_claim"
			}
			ctx := result.WithCompletionPolicy(t.Context(), func(req result.CompletionRequest) result.Completion {
				var required []string
				if mode == "extended-policy" {
					required = []string{"real_view"}
				}
				c := result.AssessCompletion(req, required, func(ref result.EvidenceReference) bool { return ref.TurnID == "manager-turn" && ref.CallID == "check" })
				c.PolicyRevision = "prepared-policy"
				return c
			})
			must(s.Judge(ctx, task.ID, receipt.EventID, "verified", "checked", proof))
			card, err := s.Get(t.Context(), task.ID)
			must(err)
			if card.State == "done" || card.Completion != nil {
				t.Fatalf("completed before application: %+v", card)
			}
			var recorded int
			must(db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM done_completions").Scan(&recorded)
			}))
			if recorded != 0 {
				t.Fatalf("acceptance recorded %d premature completions", recorded)
			}
			// Application after restart has no manager turn or prepared context.
			s = New(db)
			must(s.Rebuild(t.Context()))
			must(s.Judge(t.Context(), task.ID, receipt.EventID, "verified", "checked", proof))
			if mode == "moved-head" {
				must(s.ObserveHead(t.Context(), task.ID, "run-1", strings.Repeat("b", 40)))
			}
			must(s.Apply(t.Context(), task.ID, receipt.EventID, "merged"))
			must(s.Rebuild(t.Context()))
			must(s.Apply(t.Context(), task.ID, receipt.EventID, "merged"))
			card, err = s.Get(t.Context(), task.ID)
			must(err)
			want, breaches := "claimed", 1
			if mode == "observed" {
				want, breaches = "done", 0
			}
			if card.Completion == nil || card.Completion.State != want {
				t.Fatalf("application lost accepted evidence: %+v", card.Completion)
			}
			if mode != "moved-head" && card.Completion.PolicyRevision != "prepared-policy" {
				t.Fatalf("lost prepared policy: %+v", card.Completion)
			}
			if mode == "observed" && (len(card.Completion.Evidence) != 1 || card.Completion.Evidence[0] != proof.Evidence[0]) {
				t.Fatalf("lost references: %+v", card.Completion)
			}
			var count int
			must(db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM done_completions WHERE state='claimed'").Scan(&count)
			}))
			if count != breaches {
				t.Fatalf("breaches=%d want=%d", count, breaches)
			}
		})
	}
}

func TestTaskCompletionWithoutDoneEvidenceIsClaimed(t *testing.T) {
	t.Parallel()
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

func TestChangedDeliveryClearsCurrentCompletion(t *testing.T) {
	t.Parallel()
	s, _, receipt, _ := fileFixture(t)
	card := submitFiles(t, s, receipt)
	proof := result.CompletionRequest{WorkKind: "document", Evidence: []result.EvidenceReference{{Kind: "source_reopened", TurnID: "turn", CallID: "read"}}}
	ctx := result.WithCompletionPolicy(t.Context(), func(req result.CompletionRequest) result.Completion {
		return result.AssessCompletion(req, nil, func(result.EvidenceReference) bool { return true })
	})
	evidence, err := s.LandFiles(ctx, card.Task.ID, receipt.EventID, card.FileSnapshot.Fingerprint, "Checked source", proof)
	if err != nil {
		t.Fatal(err)
	}
	card, err = s.Get(t.Context(), card.Task.ID)
	if err != nil || card.Completion == nil || card.Completion.State != "done" {
		t.Fatalf("initial completion: %+v %v", card, err)
	}
	writeFixture(t, filepath.Join(evidence.Path, "out/result.txt"), "changed destination")
	if _, err = s.LandFiles(ctx, card.Task.ID, receipt.EventID, card.FileSnapshot.Fingerprint, "Recheck", proof); !errors.Is(err, ErrFilesChanged) {
		t.Fatal(err)
	}
	if err = s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	card, err = s.Get(t.Context(), card.Task.ID)
	if err != nil || card.Applied || card.State != "ready_for_review" || card.Completion != nil {
		t.Fatalf("invalidated delivery retained completion: %+v %v", card, err)
	}
	page, _, err := s.SummaryPage(t.Context(), card.Task.ID+":1", 0)
	if err != nil || !strings.HasPrefix(page, "ready_for_review:") {
		t.Fatalf("reopened delivery presented as done: %s %v", page, err)
	}
}

func TestTaskCompletionEvidenceSurvivesReplay(t *testing.T) {
	t.Parallel()
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
