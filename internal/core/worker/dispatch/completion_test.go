// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
)

type completionPR struct{ pr antigravity.PullRequest }

func (p completionPR) ReadBranch(context.Context, string, string) ([]byte, error) {
	return json.Marshal(p.pr)
}
func (p completionPR) ReadComments(context.Context, string, int) ([]byte, error) {
	return []byte(`[[]]`), nil
}

func TestCompletionDeliversNegativePRObservation(t *testing.T) {
	db := store(t)
	w := work.New(db)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.Create(ctx, work.Task{ID: "task", AgentID: "agent", ConversationID: "conversation", Title: "Result", Done: "file exists", ArtifactRoots: []string{"/task"}}))
	must(w.Start(ctx, work.Run{ID: "run", TaskID: "task", Generation: 1, Worker: "fixture", ThreadID: "thread", TurnID: "turn", NodeID: "node"}))
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: "task", ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: "thread", TurnID: "turn", TurnStatus: "completed"}, EmittedAt: time.Now(), Manifest: []work.Artifact{{Path: "/task/result", SHA256: strings.Repeat("a", 64)}}}
	receipt.Provenance = []work.Provenance{{ArtifactID: "artifact", TaskID: "task", NodeID: "node", Path: "/task/result", SHA256: strings.Repeat("a", 64), ObservedAt: receipt.EmittedAt}}
	receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
	receipt.EventID = work.ReceiptID(receipt)
	must(w.Submit(ctx, "run", receipt))
	pr := antigravity.PullRequest{Number: 1, URL: "https://github.com/fixture/project/pull/1", State: "OPEN", Branch: "task-branch", Base: "dev", Head: strings.Repeat("a", 40), Checks: []antigravity.Check{{Type: "CheckRun", Name: "checks", Status: "COMPLETED", Conclusion: "FAILURE"}}}
	record := Record{ID: "task", State: "finished", Code: "ready_for_review", Definition: Definition{Branch: "task-branch", Base: "dev", Repository: "/fixture"}, PR: &pr, ChangeEvidence: "old diff"}
	must(db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES('task','turn','/fixture','finished',?)", mustJSON(record))
		return err
	}))
	published := 0
	s := &Service{cfg: Config{DB: db, Work: w, PR: completionPR{pr}, Publish: func(_ context.Context, r Record) error {
		published++
		if r.Code != "pr_unverified" || r.PR == nil || r.PR.Checks[0].Conclusion != "FAILURE" || r.ChangeEvidence != "" {
			t.Errorf("negative evidence treated as verified: %+v", r)
		}
		return nil
	}}}
	must(s.finish(ctx, record, "run", "completed"))
	pending, err := w.PendingCompletions(ctx, "1")
	must(err)
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	event, card, claimed, err := w.ClaimCompletion(ctx, pending[0].ID, "1")
	must(err)
	if !claimed {
		t.Fatal("not claimed")
	}
	found, err := s.React(ctx, event, card)
	if !found || err != nil || published != 1 {
		t.Fatalf("observed failing check withheld from manager: found=%v published=%d error=%v", found, published, err)
	}
}
