// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"testing"
	"time"
)

func claimedAssignment(t *testing.T) (*Engine, Action) {
	t.Helper()
	_, db := testJournal(t)
	e, _ := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err := e.Admit(t.Context(), oneGoal(readyTask("a"))); err != nil {
		t.Fatal(err)
	}
	if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Snapshot(t.Context())
	a, err := e.Claim(t.Context(), s.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return e, a
}
func TestCommittedSignalWakesReadOnlyObservationImmediately(t *testing.T) {
	e, a := claimedAssignment(t)
	if err := e.Report(t.Context(), a.ID, 1, Observation{Status: OutcomeRunning, Handle: "worker-handle"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.cfg.Driver = &fixtureDriver{observe: func(got Action) (Observation, error) {
		calls++
		return Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true}, nil
	}}
	if err := e.Signal(t.Context(), Signal{ID: "finish-1", ActionID: a.ID}); err != nil {
		t.Fatal(err)
	}
	if err := e.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.wg.Wait()
	if calls != 1 {
		t.Fatalf("committed completion waited for sweep: calls=%d", calls)
	}
	if err := e.Signal(t.Context(), Signal{ID: "finish-1", ActionID: a.ID}); err != nil {
		t.Fatal(err)
	}
}
func TestQuotaFailoverSpendsNoRecovery(t *testing.T) {
	e, a := claimedAssignment(t)
	if err := e.Report(t.Context(), a.ID, 1, Observation{Status: OutcomeQuota, EffectsSettled: true, QuotaReset: 200}); err != nil {
		t.Fatal(err)
	}
	if err := e.NodeChanged(t.Context(), readyNode("other")); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Snapshot(t.Context())
	task := s.Goals[0].Tasks[0]
	if task.Recoveries != 0 || task.NodeID != "other" || task.Attempt != 2 {
		t.Fatalf("quota consumed recovery or reused parked worker: %+v", task)
	}
}
func TestWorkerWithoutReceiptGetsOnlyOneContinuation(t *testing.T) {
	e, a := claimedAssignment(t)
	if err := e.Report(t.Context(), a.ID, 1, Observation{Status: OutcomeNoReceipt, EffectsSettled: true, Handle: "thread"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Snapshot(t.Context())
	var continuation Action
	for _, candidate := range s.Actions {
		if candidate.State == "queued" {
			continuation = candidate
		}
	}
	if continuation.Kind != ContinueWorker {
		t.Fatalf("no one-time continuation: %+v", s.Actions)
	}
	a, err := e.Claim(t.Context(), continuation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), a.ID, 1, Observation{Status: OutcomeNoReceipt, EffectsSettled: true, Handle: "thread"}); err != nil {
		t.Fatal(err)
	}
	s, _ = e.Snapshot(t.Context())
	if s.Goals[0].Tasks[0].Stage != TaskWorkerLost {
		t.Fatal("second empty ending kept looping")
	}
}

func TestPRChangeSignalInvalidatesCachedMergeProofs(t *testing.T) {
	e, worker := claimedAssignment(t)
	if err := e.Report(t.Context(), worker.ID, 1, Observation{Status: OutcomeSucceeded, Head: "old-head", EffectsSettled: true}); err != nil {
		t.Fatal(err)
	}
	inspection := claimOnlyAction(t, e, InspectPR)
	proof := Proof{Revision: "old-head", Ref: "old-proof", Passed: true}
	if err := e.Report(t.Context(), inspection.ID, 1, Observation{Status: OutcomeSucceeded, PR: PRState{Head: "old-head", Checks: proof, Review: proof, Verified: proof, Mergeable: true}}); err != nil {
		t.Fatal(err)
	}
	// No merge has been queued yet. A change signal must still invalidate all
	// cached authority for that head before the next derivation can merge it.
	if err := e.Signal(t.Context(), Signal{ID: "head-changed", GoalID: worker.GoalID, TaskID: worker.TaskID}); err != nil {
		t.Fatal(err)
	}
	claimOnlyAction(t, e, InspectPR)
}
