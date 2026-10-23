// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"strings"
	"testing"
	"time"
)

func TestDispatchRecoversOnceOnlyAfterAllEffectsSettle(t *testing.T) {
	db := store(t)
	w := work.New(db)
	s := &Service{cfg: Config{DB: db, Work: w}}
	task := work.Task{ID: "task", PersonID: "person", AgentID: "agent", ConversationID: "conversation", Title: "Task", Done: "result exists"}
	if err := w.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	r := Record{ID: task.ID, State: "unknown", Attempt: 1, Snapshot: &worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn", TaskID: task.ID}, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "result_error"}}}
	raw, _ := json.Marshal(r)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)`, r.ID, "origin", "repository", r.State, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Start(t.Context(), work.Run{ID: "task:1", TaskID: task.ID, Generation: 1, Worker: "claude", ThreadID: "thread", TurnID: "turn", NodeID: "main"}); err != nil {
		t.Fatal(err)
	}
	if next, err := s.recoverRecord(t.Context(), r); err != nil || next.State != "unknown" || next.Attempt != 1 {
		t.Fatalf("unknown effect replayed: %+v %v", next, err)
	}
	r.Snapshot.EffectsKnown = true
	next, err := s.recoverRecord(t.Context(), r)
	if err != nil || next.State != "queued" || next.Attempt != 1 || next.Recoveries != 1 {
		t.Fatalf("settled failure did not recover: %+v %v", next, err)
	}
	card, err := w.Get(t.Context(), task.ID)
	if err != nil || card.Active || card.TurnStatus != "failed" {
		t.Fatalf("prior turn not settled: %+v %v", card, err)
	}
	if err = w.Start(t.Context(), work.Run{ID: "task:2", TaskID: task.ID, Generation: 2, Worker: "claude", ThreadID: "thread2", TurnID: "turn2", NodeID: "main"}); err != nil {
		t.Fatal(err)
	}
	next.Attempt = 2
	next.State = "unknown"
	next.Snapshot = r.Snapshot
	next.Snapshot.Thread.TurnID = "turn2"
	final, err := s.recoverRecord(t.Context(), next)
	if err != nil || final.State != "decision" || final.Attempt != 2 || final.DecisionAt == 0 {
		t.Fatalf("second failure did not require decision: %+v %v", final, err)
	}
	events, err := w.Ledger(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	starts, ends := 0, 0
	for _, e := range events {
		if e.Kind == "started" {
			starts++
		}
		if e.Kind == "finished" {
			ends++
		}
	}
	// An old completion cannot publish a terminal result for the new attempt.
	s.cfg.Publish = func(context.Context, Record) error { t.Fatal("old completion published during recovery"); return nil }
	if found, err := s.React(t.Context(), work.Completion{TaskID: task.ID, Attempt: "task:1"}, card); !found || err != nil {
		t.Fatalf("old completion not consumed: %v %v", found, err)
	}
	if starts != 2 || ends != 2 {
		t.Fatalf("attempt history lost: starts=%d ends=%d", starts, ends)
	}
}

func TestTerminalSnapshotCannotCommitWithoutItsReceipt(t *testing.T) {
	db := store(t)
	w := work.New(db)
	s := &Service{cfg: Config{DB: db, Work: w}}
	if err := w.Create(t.Context(), work.Task{ID: "task", AgentID: "agent", ConversationID: "conversation", Title: "Task", Done: "result", ArtifactRoots: []string{"/task"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Start(t.Context(), work.Run{ID: "task:1", TaskID: "task", Generation: 1, Worker: "claude", ThreadID: "thread", TurnID: "turn", NodeID: "main"}); err != nil {
		t.Fatal(err)
	}
	r := Record{ID: "task", Attempt: 1, State: "running"}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES('task','origin','repo','running',?)", mustJSON(r))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.State = "finished"
	r.Snapshot = &worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn"}, Finish: &worker.Finish{Status: worker.Done}, EffectsKnown: true}
	if err := s.finish(t.Context(), r, "task:1", "completed"); err == nil {
		t.Fatal("terminal snapshot committed without receipt evidence")
	}
	now := time.Now()
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: "task", ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: "thread", TurnID: "turn", TurnStatus: "completed"}, EmittedAt: now, Manifest: []work.Artifact{{Path: "/task/result", SHA256: strings.Repeat("a", 64)}}}
	receipt.Provenance = []work.Provenance{{ArtifactID: "adapter-derived:task", TaskID: "task", NodeID: "main", Path: "/task/result", SHA256: receipt.Manifest[0].SHA256, ObservedAt: now}}
	receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
	receipt.EventID = work.ReceiptID(receipt)
	if err := s.finish(t.Context(), r, "task:1", "invalid", receipt); err == nil {
		t.Fatal("invalid terminal observation committed")
	}
	card, err := w.Get(t.Context(), r.ID)
	if err != nil || card.Receipt != nil || !card.Active {
		t.Fatalf("partial receipt escaped rollback: %+v %v", card, err)
	}
	saved, err := s.records(t.Context())
	if err != nil || saved[0].Snapshot != nil {
		t.Fatalf("recoverable snapshot escaped rollback: %+v %v", saved, err)
	}
	if err = s.finish(t.Context(), r, "task:1", "completed", receipt); err != nil {
		t.Fatal(err)
	}
	card, err = w.Get(t.Context(), r.ID)
	if err != nil || card.Receipt == nil || card.Active {
		t.Fatalf("terminal receipt missing: %+v %v", card, err)
	}
	saved, err = s.records(t.Context())
	if err != nil || saved[0].Snapshot == nil || saved[0].State != "finished" {
		t.Fatalf("terminal snapshot missing: %+v %v", saved, err)
	}

}

func TestQuotaReplacementKeepsOneOrdinaryRecovery(t *testing.T) {
	thread := worker.Thread{ID: "thread", TurnID: "turn", TaskID: "task", AttemptID: "task:2"}
	r := Record{ID: "task", State: "unknown", Attempt: 2, Thread: &thread, Tried: []string{"first", "second"}, Packet: "original packet", Snapshot: &worker.ThreadSnapshot{Thread: thread, EffectsKnown: true, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure}}}
	next := recoveryRecord(r)
	if next.State != "queued" || next.Recoveries != 1 || next.Attempt != 2 || len(next.Tried) != 0 || next.RecoveryThread == nil {
		t.Fatalf("quota replacement consumed recovery: %+v", next)
	}
	next.Attempt = 3
	next.Snapshot = r.Snapshot
	if final := recoveryRecord(next); final.State != "decision" || final.Recoveries != 1 {
		t.Fatalf("ordinary recovery allowance reset: %+v", final)
	}
}

func TestDispatchCancellationSurvivesStaleObservation(t *testing.T) {
	db := store(t)
	s := &Service{cfg: Config{DB: db}}
	r := Record{ID: "task", State: "unknown", Attempt: 1}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES('task','turn','repo','unknown',?)", mustJSON(r))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	r.Snapshot = &worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn"}, EffectsKnown: true, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure}}
	if err := s.save(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	saved, err := s.records(t.Context())
	if err != nil || len(saved) != 1 || !saved[0].Canceled {
		t.Fatalf("cancel lost: %+v %v", saved, err)
	}
	if next := recoveryRecord(saved[0]); next.State != "finished" || next.Recoveries != 0 {
		t.Fatalf("canceled work recovered: %+v", next)
	}
}
