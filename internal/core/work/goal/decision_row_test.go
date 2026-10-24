// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"strings"
	"testing"
	"time"
)

func TestRevisionClearsRecordedDecisionRow(t *testing.T) {
	t.Parallel()
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
	queued, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Actions) != 1 || queued.Actions[0].Kind != PlanGoal {
		t.Fatalf("liveness turn was not queued: %+v", queued.Actions)
	}
	const decision = "Which city should the trip use?"
	claimed, err := e.Claim(t.Context(), queued.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), claimed.ID, claimed.Epoch, Observation{Status: OutcomeSucceeded, Decision: decision}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	recorded, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Goals[0].State != GoalBlocked || recorded.Goals[0].DecisionRow != decision {
		t.Fatalf("decision was not recorded: %+v", recorded.Goals[0])
	}

	saved := recorded.Goals[0]
	r, rev, err := db.Definitions.Read(t.Context(), saved.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := db.Definitions.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: append(r.Data, []byte("\nChanged outcome from the person.\n")...)}, saved.Person, saved.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	revised, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	g := revised.Goals[0]
	if g.RecordRevision != edited || !g.DefinitionPending {
		t.Fatalf("revision was not fenced: %+v", g)
	}
	if g.DecisionRow != "" || g.BlockedReason == decision || g.State == GoalBlocked {
		t.Fatalf("revision kept the recorded decision: %+v", g)
	}
}

func TestCorrectedPlanClearsRecordedDecisionRow(t *testing.T) {
	t.Parallel()
	g := oneGoal()
	g.RecordRevision, g.DefinitionPending = "corrected", true
	g.DecisionRow = "Which city should the trip use?"
	g.AttentionID = "goal:g:decision"
	g.BlockedReason = g.DecisionRow
	g.State = GoalBlocked
	if err := applyCorrectionPlan(&g, Observation{Tasks: []Task{readyTask("next")}}); err != nil {
		t.Fatal(err)
	}
	if g.DecisionRow != "" || g.AttentionID != "" || g.BlockedReason != "" || g.State != GoalRunning || g.DefinitionPending {
		t.Fatalf("corrected plan kept the recorded decision: %+v", g)
	}
	plan, err := Derive(State{Goals: []Goal{g}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if has(plan.Rows, g.ID) || strings.Contains(g.BlockedReason, "city") {
		t.Fatalf("old decision returned after the corrected plan: %+v rows=%v", g, plan.Rows)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].TaskID != "next" || plan.Actions[0].Kind != Assign {
		t.Fatalf("corrected work did not start: %+v", plan.Actions)
	}
}
