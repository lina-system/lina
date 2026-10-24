// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
)

// TestGoalLineCarriesNowStartAndStages reads one goal from the ledger and checks
// the line fields the progress topic publishes: what it is doing now, when its
// work started and its numbered stage entries.
func TestGoalLineCarriesNowStartAndStages(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	repo, err := records.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	engine, err := goal.NewEngine(goal.Config{DB: db, Records: stageDefinitions{repo}, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	admitGoal(t, engine)
	step := func(id, name string, blockers ...string) goal.Step {
		return goal.Step{ID: id, Name: name, Done: "Produce " + name, Repository: "repo", Base: "dev", Area: id + ".txt", Worker: "antigravity", Resources: goal.Resources{CPU: 0.01, MemoryMB: 1, DiskMB: 1}, DependsOn: blockers}
	}
	if _, err = engine.SubmitStepGraph(t.Context(), "g", "person", "agent", "conversation", 0, []goal.Step{step("stay", "숙소 고르기"), step("move", "이동 준비", "stay")}); err != nil {
		t.Fatal(err)
	}
	var units []Unit
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		units, err = Goals(t.Context(), tx, "person", map[string]string{"agent": "Agent"}, func(id string) string {
			if id == "g" {
				return "Prepare the trip"
			}
			return ""
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("goal units = %+v", units)
	}
	u := units[0]
	if u.ID != "g" || u.Kind != "project" || u.Name != "Prepare the trip" || !u.Active {
		t.Fatalf("goal unit = %+v", u)
	}
	if !u.StartedAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("started = %v, want the goal's hand-over instant", u.StartedAt)
	}
	if len(u.Stages) != 3 || u.Stages[0].Number != 1 || u.Stages[1].Number != 2 || u.Stages[2].Kind != KindChecking {
		t.Fatalf("stage entries = %+v", u.Stages)
	}
	if u.Percent == nil || *u.Percent != 0 {
		t.Fatalf("percent = %v, want zero for no landed step", u.Percent)
	}
	entries := Compute(units)
	if len(entries) != 1 {
		t.Fatalf("lines = %+v", entries)
	}
	line := entries[0].Line
	if line.Stage != u.Stages[0].Label || len(line.Stages) != 3 {
		t.Fatalf("line stage = %q entries = %+v", line.Stage, line.Stages)
	}
	if line.Now != design.Text(design.English, "progress.waiting_word") {
		t.Fatalf("now = %q, want the waiting word", line.Now)
	}
	if !line.StartedAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("line started = %v", line.StartedAt)
	}
	// Another person's goal is not observed.
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		units, err = Goals(t.Context(), tx, "stranger", map[string]string{"agent": "Agent"}, func(id string) string {
			if id == "g" {
				return "Prepare the trip"
			}
			return ""
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(units) != 0 {
		t.Fatalf("foreign goal leaked: %+v", units)
	}
}

func admitGoal(t *testing.T, engine *goal.Engine) {
	t.Helper()
	g := goal.Goal{ID: "g", Person: "person", Agent: "agent", Conversation: "conversation", Summary: "Prepare the trip", Done: "The trip is booked", NoServiceReason: "plan", Scope: goal.GoalScope{Repositories: []string{"repo"}, Includes: "plan.md"}, Criteria: []goal.Criterion{{ID: "outcome", Statement: "The trip is booked", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "repo"}}}}
	if err := engine.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
}
