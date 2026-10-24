// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRecordedWaitAndDecisionHoldGoalClosure(t *testing.T) {
	t.Parallel()
	for _, hold := range []string{"reply", "deadline", "decision"} {
		t.Run(hold, func(t *testing.T) {
			g := finishedGoal()
			for _, c := range goalCriteria(g) {
				g.Checks = append(g.Checks, CriterionEvidence{Criterion: c.ID, GoalRevision: g.Revision, Round: g.CheckRound, Target: c.Check.Target, Revision: criterionRevision(g, c), Kind: c.Check.Kind, Collected: &CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "checked"}, Ref: "read:" + c.ID, Passed: true})
			}
			if hold == "decision" {
				g.DecisionRow = "The host has not replied. Decide whether to continue."
			} else {
				w := outsideWait()
				w.Recorded = true
				if hold == "deadline" {
					w.Until = 100
				}
				g.Waits = []Wait{w}
			}
			plan, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Close) != 0 {
				t.Fatalf("closed with unresolved %s: %+v", hold, plan)
			}
			for _, a := range plan.Actions {
				if a.Kind != ReadSource {
					t.Fatalf("started final checking before %s resolved: %s", hold, a.Kind)
				}
			}
		})
	}
}

func TestMultipleExpiredWaitsKeepEveryRecommendation(t *testing.T) {
	first, second := outsideWait(), outsideWait()
	first.Recorded, second.Recorded = true, true
	second.Wake = "booking.changed"
	second.Recommendation, second.Draft = "The booking is unconfirmed.", "Please confirm the booking."
	g := oneGoal()
	g.Waits = []Wait{first, second}
	for _, w := range []Wait{first, second} {
		if err := applyWaitRead(&g, Action{TaskID: waitTaskID(w.Wake), ReadCause: "deadline"}, Observation{SourceOpen: boolPtr(true)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(g.Waits) != 0 || g.State != GoalBlocked || g.AttentionID == "" {
		t.Fatalf("expired waits did not become a row: %+v", g)
	}
	for _, text := range []string{first.Recommendation, first.Draft, second.Recommendation, second.Draft} {
		if !strings.Contains(g.DecisionRow, text) || !strings.Contains(g.BlockedReason, text) {
			t.Fatalf("expiry lost a recommendation or draft: %q", g.DecisionRow)
		}
	}
}

func TestManagerRecordsMultipleOutsideWaitsAtomically(t *testing.T) {
	t.Parallel()
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "independent", true: "duplicate"}[duplicate], func(t *testing.T) {
			_, db := testJournal(t)
			e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Admit(t.Context(), oneGoal()); err != nil {
				t.Fatal(err)
			}
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			s, err := e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			a, err := e.Claim(t.Context(), s.Actions[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			first, second := outsideWait(), outsideWait()
			if !duplicate {
				second.Wake, second.Source, second.TodoID = "booking.changed", "mail:booking", "booking-reply"
			}
			raw, err := json.Marshal(map[string]any{"status": OutcomeSucceeded, "wait": first, "waits": []Wait{second}})
			if err != nil {
				t.Fatal(err)
			}
			var o Observation
			if err = json.Unmarshal(raw, &o); err != nil {
				t.Fatal(err)
			}
			if duplicate {
				o.Tasks = []Task{readyTask("must-not-start")}
			}
			err = e.Report(t.Context(), a.ID, 1, o)
			if duplicate {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("duplicate wake was not refused: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			s, err = e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if duplicate {
				want = 0
			}
			if len(s.Goals[0].Waits) != want {
				t.Fatalf("waits=%d, want %d", len(s.Goals[0].Waits), want)
			}
			if duplicate {
				if len(s.Goals[0].Tasks) != 0 || s.Goals[0].ManagerTurns != 0 {
					t.Fatal("invalid wait set partially committed its plan")
				}
				return
			}
			if err = e.Signal(t.Context(), Signal{ID: "host-replied", GoalID: "g", Wake: first.Wake}); err != nil {
				t.Fatal(err)
			}
			s, err = e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			reads := sourceReads(s)
			if len(reads) != 1 || reads[0].WaitSource != first.Source {
				t.Fatalf("signal read another wait: %+v", reads)
			}
			if _, err = e.Claim(t.Context(), reads[0].ID); err != nil {
				t.Fatal(err)
			}
			if err = e.Report(t.Context(), reads[0].ID, 1, Observation{Status: OutcomeSucceeded, SourceOpen: boolPtr(false)}); err != nil {
				t.Fatal(err)
			}
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			s, err = e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if s.Goals[0].State != GoalWaiting || len(s.Goals[0].Waits) != 1 || s.Goals[0].Waits[0].Source != second.Source {
				t.Fatalf("host signal lost the booking wait: %+v", s.Goals[0])
			}
		})
	}
}

func TestRecordedWaitStillAllowsIndependentTask(t *testing.T) {
	t.Parallel()
	g := oneGoal(readyTask("independent"))
	w := outsideWait()
	w.Recorded = true
	g.Waits = []Wait{w}
	plan, err := Derive(State{Goals: []Goal{g}, Nodes: []Node{readyNode("node")}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != Assign || plan.Actions[0].TaskID != "independent" {
		t.Fatalf("outside wait stopped independent work: %+v", plan)
	}
}
