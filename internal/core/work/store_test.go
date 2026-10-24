// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func testStore(t *testing.T) (*Service, *canon.Store) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return New(db), db
}
func taskFixture() Task {
	return Task{ID: "task-1", AgentID: "agent-1", ConversationID: "conversation-1", Title: "Produce report", Done: "report exists", ArtifactRoots: []string{"/work/task-1"}}
}
func runFixture() Run {
	return Run{ID: "run-1", TaskID: "task-1", Generation: 1, Worker: "fixture", ThreadID: "thread-1", TurnID: "turn-1", NodeID: "node-1"}
}
func receiptFixture() Receipt {
	r := Receipt{SchemaVersion: 1, RelationshipID: "task-1", ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: TurnRef{ThreadID: "thread-1", TurnID: "turn-1", TurnStatus: "completed"}, EmittedAt: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), Manifest: []Artifact{{Path: "/work/task-1/report.txt", SHA256: strings.Repeat("a", 64)}}}
	r.RevisionHash = ManifestHash(r.Manifest)
	r.EventID = ReceiptID(r)
	r.Provenance = []Provenance{{ArtifactID: "artifact-1", TaskID: "task-1", NodeID: "node-1", Path: r.Manifest[0].Path, SHA256: r.Manifest[0].SHA256, ObservedAt: r.EmittedAt}}
	return r
}
func startFixture(t *testing.T, s *Service) {
	t.Helper()
	if err := s.Create(t.Context(), taskFixture()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), runFixture()); err != nil {
		t.Fatal(err)
	}
}

func TestOneActiveRunConcurrent(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	if err := s.Create(t.Context(), taskFixture()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, id := range []string{"run-1", "run-2"} {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; r := runFixture(); r.ID = id; results <- s.Start(t.Context(), r) }()
	}
	close(start)
	wg.Wait()
	close(results)
	success, refused := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrActiveRun) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || refused != 1 {
		t.Fatalf("success=%d refused=%d", success, refused)
	}
}
func TestReceiptProvenanceAndBinding(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Receipt){"missing": func(r *Receipt) { r.Provenance = nil }, "task": func(r *Receipt) { r.Provenance[0].TaskID = "other" }, "node": func(r *Receipt) { r.Provenance[0].NodeID = "other" }, "hash": func(r *Receipt) { r.Provenance[0].SHA256 = strings.Repeat("b", 64) }, "time": func(r *Receipt) { r.Provenance[0].ObservedAt = time.Time{} }, "artifact": func(r *Receipt) { r.Provenance[0].ArtifactID = "" }, "generation": func(r *Receipt) { r.ExecutionGeneration = 2 }, "thread": func(r *Receipt) { r.TurnRef.ThreadID = "other" }, "manifest digest": func(r *Receipt) { r.RevisionHash = strings.Repeat("b", 64) }, "event identity": func(r *Receipt) { r.EventID = strings.Repeat("b", 32) }, "outside scope": func(r *Receipt) {
		r.Manifest[0].Path = "/outside/report.txt"
		r.Provenance[0].Path = r.Manifest[0].Path
		r.RevisionHash = ManifestHash(r.Manifest)
		r.EventID = ReceiptID(*r)
	}}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := testStore(t)
			startFixture(t, s)
			before, _ := s.Ledger(t.Context(), "task-1")
			r := receiptFixture()
			mutate(&r)
			if err := s.Submit(t.Context(), "run-1", r); err == nil {
				t.Fatal("invalid receipt accepted")
			}
			after, _ := s.Ledger(t.Context(), "task-1")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejection changed ledger")
			}
		})
	}
}
func TestProjectionRebuildAndAcceptance(t *testing.T) {
	t.Parallel()
	s, db := testStore(t)
	startFixture(t, s)
	ctx := t.Context()
	r := receiptFixture()
	if err := s.Submit(ctx, "run-1", r); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(ctx, "run-1", r); err != nil {
		t.Fatal("idempotent receipt", err)
	}
	if err := s.Judge(ctx, "task-1", r.EventID, "verified", "review-1"); !errors.Is(err, ErrState) {
		t.Fatalf("premature judgment: %v", err)
	}
	if err := s.Finish(ctx, "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	live, err := s.Get(ctx, "task-1")
	if err != nil || live.State != "working" || live.Verdict != "" {
		t.Fatalf("claim became acceptance: %+v %v", live, err)
	}
	if err := s.Judge(ctx, "task-1", r.EventID, "verified", "review-1"); err != nil {
		t.Fatal(err)
	}
	live, err = s.Get(ctx, "task-1")
	if err != nil || live.State != "done" {
		t.Fatalf("accepted: %+v %v", live, err)
	}
	ledger, err := s.Ledger(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := Project(ledger)
	if err != nil || !reflect.DeepEqual(live, rebuilt) {
		t.Fatalf("replay: %+v %+v %v", live, rebuilt, err)
	}
	if err = db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM work_cards; DELETE FROM work_active_runs")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := New(db).Get(ctx, "task-1")
	if err != nil || !reflect.DeepEqual(live, restored) {
		t.Fatalf("restored: %+v %v", restored, err)
	}
	if err = db.Write(ctx, func(tx *canon.Tx) error { _, err := tx.ExecContext(ctx, "DELETE FROM work_ledger"); return err }); err == nil {
		t.Fatal("append-only ledger allowed delete")
	}
}
func TestGenerationDoesNotInheritAcceptance(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	ctx := t.Context()
	task := taskFixture()
	task.RequiresApply = true
	if err := s.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx, runFixture()); err != nil {
		t.Fatal(err)
	}
	r := receiptFixture()
	if err := s.Submit(ctx, "run-1", r); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := s.Judge(ctx, "task-1", r.EventID, "verified", "review-1"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(ctx, "task-1")
	if c.State != "ready_for_review" {
		t.Fatalf("unapplied state: %s", c.State)
	}
	next := runFixture()
	next.ID = "run-2"
	next.Generation = 2
	next.TurnID = "turn-2"
	if err := s.Start(ctx, next); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(ctx, "task-1")
	if c.Verdict != "" || c.Receipt != nil || c.State != "working" {
		t.Fatalf("stale acceptance: %+v", c)
	}
	if err := s.Submit(ctx, "run-1", r); !errors.Is(err, ErrState) {
		t.Fatalf("old run receipt: %v", err)
	}
	if err := s.Apply(ctx, "task-1", r.EventID, "apply-1"); !errors.Is(err, ErrState) {
		t.Fatalf("old revision applied: %v", err)
	}
}

func TestRestartPreservesActiveReservation(t *testing.T) {
	t.Parallel()
	s, db := testStore(t)
	startFixture(t, s)
	ctx := t.Context()
	if err := New(db).Recover(ctx, "epoch-2"); err != nil {
		t.Fatal(err)
	}
	c, err := s.Get(ctx, "task-1")
	if err != nil || c.State != "unknown" || !c.Active {
		t.Fatalf("recovery: %+v %v", c, err)
	}
	next := runFixture()
	next.ID = "run-2"
	next.Generation = 2
	if err := s.Start(ctx, next); !errors.Is(err, ErrActiveRun) {
		t.Fatalf("recovery released unobserved run: %v", err)
	}
	if err := s.Finish(ctx, "task-1", "run-1", "interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx, next); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptWithWallClockRetries(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	startFixture(t, s)
	r := receiptFixture()
	r.EmittedAt = time.Now()
	r.Provenance[0].ObservedAt = r.EmittedAt
	if err := s.Submit(t.Context(), "run-1", r); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(t.Context(), "run-1", r); err != nil {
		t.Fatalf("clock metadata broke idempotency: %v", err)
	}
}

func TestReceiptAndEndOrder(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	startFixture(t, s)
	ctx := t.Context()
	if err := s.Finish(ctx, "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(ctx, "task-1")
	if c.State != "unknown" {
		t.Fatal("turn end inferred a delivery")
	}
	r := receiptFixture()
	if err := s.Submit(ctx, "run-1", r); err != nil {
		t.Fatal(err)
	}
	if err := s.Judge(ctx, "task-1", r.EventID, "verified", "review-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(ctx, "run-1", r); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(ctx, "task-1")
	if c.State != "done" {
		t.Fatal("retry erased acceptance")
	}
}

func TestApplyRequiresMatchingVerifiedReceipt(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	ctx := t.Context()
	task := taskFixture()
	task.RequiresApply = true
	if err := s.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx, runFixture()); err != nil {
		t.Fatal(err)
	}
	r := receiptFixture()
	if err := s.Submit(ctx, "run-1", r); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, "task-1", r.EventID, "apply-1"); !errors.Is(err, ErrState) {
		t.Fatalf("unverified apply: %v", err)
	}
	if err := s.Judge(ctx, "task-1", r.EventID, "verified", "review-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, "task-1", "other", "apply-1"); !errors.Is(err, ErrState) {
		t.Fatalf("wrong receipt apply: %v", err)
	}
	if err := s.Apply(ctx, "task-1", r.EventID, "apply-1"); err != nil {
		t.Fatal(err)
	}
	c, err := s.Get(ctx, "task-1")
	if err != nil || !c.Applied || c.State != "done" {
		t.Fatalf("applied: %+v %v", c, err)
	}
}
