// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/review"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
)

func TestReturnDefinitionOncePreservesAssignment(t *testing.T) {
	db := store(t)
	ctx := t.Context()
	rr := review.Record{Key: review.Key{Repository: "o/r", Number: 7, Head: strings.Repeat("a", 40)}, Task: "task", Receipt: "receipt", Generation: 2, State: "complete", Verdict: "needs_changes", Evidence: "Missing behavior.", Findings: []review.Finding{{ID: "major", Severity: "P1", File: "result.txt", Claim: "Missing result."}, {ID: "minor", Severity: "P2", File: "result.txt", Claim: "Optional cleanup."}}}
	r := Record{ID: "task", State: "finished", Owner: "dev", Origin: Origin{Person: "person"}, Worker: "codex", Tried: []string{"antigravity", "codex"}, Packet: "original packet", BaseCommit: strings.Repeat("b", 40), Branch: "task.branch", PR: &antigravity.PullRequest{Head: rr.Head}, Definition: Definition{Repository: "/repo"}}
	card := work.Card{Task: work.Task{ID: r.ID, AgentID: r.Owner, PersonID: r.Origin.Person}, Run: &work.Run{Generation: 2, Worker: "codex"}, Receipt: &work.Receipt{EventID: rr.Receipt}, Head: rr.Head, CompletionHead: rr.Head, Verdict: "needs_changes"}
	if err := db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO work_reviews(repository,pr_number,head,record) VALUES(?,?,?,?)", rr.Repository, rr.Number, rr.Head, mustJSON(rr)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)", r.ID, "turn", "/repo", r.State, mustJSON(r)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO work_cards(task_id,record) VALUES(?,?)", r.ID, mustJSON(card))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{cfg: Config{DB: db}, wake: make(chan struct{}, 1)}
	if err := s.ReturnDefinition(ctx, r, rr); err != nil {
		t.Fatal(err)
	}
	s = &Service{cfg: Config{DB: db}, wake: make(chan struct{}, 1)}
	if err := s.ReturnDefinition(ctx, r, rr); err != nil {
		t.Fatal("identical return must be idempotent", err)
	}
	var got Record
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch WHERE id=?", r.ID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &got)
	}); err != nil {
		t.Fatal(err)
	}
	if got.State != "queued" || got.ReturnReceipt != rr.Receipt || got.GenerationBase != 2 || got.Branch != r.Branch || got.BaseCommit != r.BaseCommit || got.Worker != "codex" || len(got.Tried) != 0 {
		t.Fatalf("return lost assignment: %+v", got)
	}
	if strings.Contains(got.Packet, `"minor"`) || !strings.Contains(got.Packet, `"major"`) || !strings.Contains(got.Packet, "original packet") {
		t.Fatal(got.Packet)
	}
	rr.Receipt = "second-receipt"
	if err := s.ReturnDefinition(ctx, r, rr); err == nil {
		t.Fatal("second return accepted")
	}
}
