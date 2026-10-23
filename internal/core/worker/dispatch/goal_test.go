// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
)

func TestGoalCompletionRemainsOwnedByGoalEngine(t *testing.T) {
	db := store(t)
	ctx := t.Context()
	origin := Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:goal"}
	r := Record{ID: admissionID(origin, "action"), Origin: origin, Goal: true, State: "finished", Code: "ready_for_review"}
	if err := db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)", r.ID, origin.Turn, "/fixture", r.State, mustJSON(r))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	published := 0
	s := &Service{cfg: Config{DB: db, Publish: func(context.Context, Record) error { published++; return nil }}}
	for range 2 {
		found, err := s.React(ctx, work.Completion{TaskID: r.ID, Attempt: "run"}, work.Card{Run: &work.Run{ID: "run"}})
		if !found || err != nil || published != 0 {
			t.Fatalf("goal completion escaped to generic manager: found=%v published=%d error=%v", found, published, err)
		}
	}
	got, err := s.GoalRecord(ctx, origin, "action")
	if err != nil || got.State != r.State || got.Code != r.Code || got.Announced {
		t.Fatalf("goal receipt changed during generic completion: %+v %v", got, err)
	}
}

func TestGoalWorkerRetainsUncertainReceipt(t *testing.T) {
	db := store(t)
	origin := Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:goal"}
	d := definition(t)
	r := Record{ID: admissionID(origin, "action"), Origin: origin, Definition: d, State: "unknown", Code: "interrupted_execution"}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)", r.ID, origin.Turn, d.Repository, r.State, mustJSON(r))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), Config{DB: db, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(t.Context())
	got, err := s.RunGoal(t.Context(), origin, "action", d)
	if err != nil || got.State != "unknown" || got.Code != r.Code {
		t.Fatalf("uncertain worker was replayed: %+v %v", got, err)
	}
	d.Done = "different goal"
	if _, err := s.RunGoal(t.Context(), origin, "action", d); err == nil {
		t.Fatal("receipt key accepted changed work")
	}
}
