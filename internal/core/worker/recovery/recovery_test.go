// SPDX-License-Identifier: Apache-2.0
package recovery

import (
	"github.com/thisisjun786/lina/internal/core/worker"
	"testing"
)

func TestRecoveryWaitsForAllEffectsAndAllowsOnlyOneNewAttempt(t *testing.T) {
	r := Record{ID: "thread", Thread: worker.Thread{ID: "thread", TurnID: "first"}, State: "unknown"}
	snap := worker.ThreadSnapshot{Thread: r.Thread, State: worker.Idle, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure}}
	if _, send := Observe(r, snap); send {
		t.Fatal("unknown effects dispatched again")
	}
	snap.EffectsKnown = true
	next, send := Observe(r, snap)
	if !send || next.Retries != 1 || next.RetryFrom != "first" {
		t.Fatalf("first settled failure did not recover: %+v", next)
	}
	if _, send = Observe(next, snap); send {
		t.Fatal("repeated observation resent continuation")
	}
	next.State = "unknown" // crash after committing the send intent
	if _, send = Observe(next, snap); send {
		t.Fatal("restart resent continuation")
	}
	next.Thread.TurnID = "second"
	snap.Thread = next.Thread
	next, send = Observe(next, snap)
	if send || next.State != "decision" {
		t.Fatalf("second failure did not become a decision: %+v", next)
	}
}
func TestRecoveryRefusesUnsettledForeignCanceledAndQuotaTurns(t *testing.T) {
	for _, kind := range []string{"missing", "foreign", "pending", "lost-request", "canceled", "quota", "completed"} {
		t.Run(kind, func(t *testing.T) {
			r := Record{Thread: worker.Thread{ID: "thread", TurnID: "turn"}, State: "unknown"}
			s := worker.ThreadSnapshot{Thread: r.Thread, EffectsKnown: true, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure}}
			switch kind {
			case "missing":
				s.Finish = nil
			case "foreign":
				s.Thread.BindingEpoch++
			case "pending":
				s.Pending = []worker.Request{{ID: "pending"}}
			case "lost-request":
				r.PendingRequests = map[string]bool{"old-approval": true}
			case "canceled":
				r.Canceled = true
			case "quota":
				s.Finish.Failure = worker.QuotaFailure
			case "completed":
				s.Finish = &worker.Finish{Status: worker.Done}
			}
			if got, send := Observe(r, s); send || got.Retries != 0 {
				t.Fatalf("unauthorized retry: %+v", got)
			}
		})
	}
}
