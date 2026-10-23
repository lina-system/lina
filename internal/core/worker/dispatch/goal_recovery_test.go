// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestGoalRecoveryReachesSchedulerWithoutConversationCall(t *testing.T) {
	for _, tc := range []struct {
		name                                                  string
		goal, recovery, allowed, authError, canceled, unknown bool
		wantClaim                                             bool
	}{
		{name: "authorized recovery", goal: true, recovery: true, allowed: true, wantClaim: true},
		{name: "ordinary uncommitted call", recovery: true, allowed: true},
		{name: "initial goal admission", goal: true, allowed: true},
		{name: "scope changed", goal: true, recovery: true},
		{name: "authority unavailable", goal: true, recovery: true, authError: true},
		{name: "canceled", goal: true, recovery: true, allowed: true, canceled: true},
		{name: "unknown effects", goal: true, recovery: true, allowed: true, unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := store(t)
			origin := Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:goal"}
			r := Record{ID: admissionID(origin, "action"), Origin: origin, Goal: tc.goal, GoalAction: "action", State: "queued"}
			if tc.recovery {
				r.State, r.Attempt = "unknown", 1
				r.Snapshot = &worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn"}, EffectsKnown: !tc.unknown, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure}}
				r = recoveryRecord(r)
				if !tc.unknown && (r.State != "queued" || r.Recoveries != 1) {
					t.Fatalf("recovery was not durably queueable: %+v", r)
				}
			}
			r.Canceled = tc.canceled
			if err := db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES(?,?,?,?,?)", r.ID, origin.Turn, "/fixture", r.State, mustJSON(r))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			claims, authorizations := 0, 0
			s := &Service{ctx: t.Context(), running: map[string]bool{}, cfg: Config{DB: db,
				AuthorizeRecovery: func(context.Context, Record) (bool, error) {
					authorizations++
					if tc.authError {
						return false, errors.New("authority unavailable")
					}
					return tc.allowed, nil
				},
				// Zero capacity makes admission observable without launching a child.
				Capacity: func(context.Context) int { claims++; return 0 },
			}}
			s.sync()
			got, err := s.GoalRecord(t.Context(), origin, "action")
			if err != nil {
				t.Fatal(err)
			}
			if (claims == 1) != tc.wantClaim || tc.wantClaim && (got.Wait != "host_capacity" || authorizations != 1) {
				t.Fatalf("goal recovery never reached scheduling: claims=%d authorized=%d state=%s wait=%s recoveries=%d", claims, authorizations, got.State, got.Wait, got.Recoveries)
			}
		})
	}
}
