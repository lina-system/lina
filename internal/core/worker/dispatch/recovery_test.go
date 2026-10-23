// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"testing"
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
	if err != nil || next.State != "queued" || next.Attempt != 2 {
		t.Fatalf("settled failure did not recover: %+v %v", next, err)
	}
	card, err := w.Get(t.Context(), task.ID)
	if err != nil || card.Active || card.TurnStatus != "failed" {
		t.Fatalf("prior turn not settled: %+v %v", card, err)
	}
	if err = w.Start(t.Context(), work.Run{ID: "task:2", TaskID: task.ID, Generation: 2, Worker: "claude", ThreadID: "thread2", TurnID: "turn2", NodeID: "main"}); err != nil {
		t.Fatal(err)
	}
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
	if starts != 2 || ends != 2 {
		t.Fatalf("attempt history lost: starts=%d ends=%d", starts, ends)
	}
}
