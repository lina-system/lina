// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func pendingVerification(t *testing.T, service bool) (*Engine, Config, Action) {
	t.Helper()
	_, db := testJournal(t)
	cfg := Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }}
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g := finishedGoal()
	g.Verification = Proof{}
	if service {
		g.NoServiceReason = ""
		g.ServiceTarget = "private-service"
		g.Deployment = Proof{Revision: g.TargetRevision, Ref: "deploy-output", Passed: true}
	}
	if err = e.prepareDefinition(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if err = e.journal.Update(t.Context(), Transition{ID: "fixture", Kind: "fixture", At: 100}, func(v *View) error { return put(v, "goal", g.ID, g) }); err != nil {
		t.Fatal(err)
	}
	return e, cfg, claimOnlyAction(t, e, Verify)
}

func claimOnlyAction(t *testing.T, e *Engine, kind ActionKind) Action {
	t.Helper()
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var queued []Action
	for _, a := range s.Actions {
		if a.State != "finished" {
			queued = append(queued, a)
		}
	}
	if len(queued) != 1 || queued[0].Kind != kind || queued[0].State != "queued" {
		t.Fatalf("want one queued %s, got %+v; goal=%+v", kind, queued, s.Goals[0])
	}
	a, err := e.Claim(t.Context(), queued[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFailedVerificationRequestsManagerBeforePerson(t *testing.T) {
	for _, service := range []bool{false, true} {
		name := "post_merge"
		if service {
			name = "post_deploy"
		}
		t.Run(name, func(t *testing.T) {
			e, cfg, a := pendingVerification(t, service)
			failed := Observation{Status: OutcomeFailed, Proof: Proof{Revision: a.Revision, Ref: "failed-runtime-output"}}
			for range 2 {
				if err := e.Report(t.Context(), a.ID, 1, failed); err != nil {
					t.Fatal(err)
				}
			}
			s, err := e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			g := s.Goals[0]
			if g.Verification != failed.Proof || len(g.Evidence) != 1 || g.Evidence[0] != failed.Proof || s.Actions[0].State != "finished" {
				t.Fatalf("failed verification did not retain evidence and finish: goal=%+v actions=%+v", g, s.Actions)
			}
			// Restart before assessment; the failure must survive and produce one
			// manager action, never another probe or an immediate person row.
			cfg.Epoch++
			e, err = NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			assessment := claimOnlyAction(t, e, PlanGoal)
			if assessment.FailureKey == "" || assessment.Revision != a.Revision || assessment.TaskID != "" {
				t.Fatalf("unscoped assessment: %+v", assessment)
			}
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			s, err = e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Actions) != 2 || s.Goals[0].State == GoalBlocked || s.Goals[0].AttentionID != "" {
				t.Fatalf("assessment duplicated or asked person: %+v", s)
			}
			// A manager that finds no repair may block exactly this failure.
			if err = e.Report(t.Context(), assessment.ID, cfg.Epoch, Observation{Status: OutcomeSucceeded}); err != nil {
				t.Fatal(err)
			}
			cfg.Epoch++
			e, err = NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err = e.Reconcile(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			s, err = e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			g = s.Goals[0]
			if g.State != GoalBlocked || g.AttentionID == "" || g.AssessedFailure != assessment.FailureKey || len(s.Actions) != 2 {
				t.Fatalf("missing assessed block: %+v", s)
			}
		})
	}
}

func TestFailedVerificationRejectsInvalidProof(t *testing.T) {
	for _, proof := range []Proof{{}, {Revision: "merged", Ref: " "}, {Revision: "old", Ref: "output"}, {Revision: "merged", Ref: "output", Passed: true}} {
		t.Run(proof.Ref+proof.Revision, func(t *testing.T) {
			e, _, a := pendingVerification(t, true)
			if err := e.Report(t.Context(), a.ID, 1, Observation{Status: OutcomeFailed, Proof: proof}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid failure accepted: %v", err)
			}
			s, err := e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Goals[0].Evidence) != 0 || s.Actions[0].State != "claimed" {
				t.Fatalf("invalid proof mutated state: %+v", s)
			}
		})
	}
}

func TestFailedVerificationRepairStaysInScope(t *testing.T) {
	e, _, a := pendingVerification(t, true)
	if err := e.Report(t.Context(), a.ID, 1, Observation{Status: OutcomeFailed, Proof: Proof{Revision: a.Revision, Ref: "probe-output"}}); err != nil {
		t.Fatal(err)
	}
	assessment := claimOnlyAction(t, e, PlanGoal)
	repair := readyTask("runtime-repair")
	repair.Repository = "outside-scope"
	if err := e.Report(t.Context(), assessment.ID, 1, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-scope repair: %v", err)
	}
	repair.Repository = "repo"
	if err := e.Report(t.Context(), assessment.ID, 1, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}}); err != nil {
		t.Fatal(err)
	}
	if err := e.NodeChanged(context.Background(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	assigned := claimOnlyAction(t, e, Assign)
	if assigned.TaskID != repair.ID {
		t.Fatalf("wrong repair assigned: %+v", assigned)
	}
}
