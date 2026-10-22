// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

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
