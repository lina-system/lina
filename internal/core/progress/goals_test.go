// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"os"
	"path/filepath"
	"reflect"
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
	// The overview must share one snapshot without mixing revisions of goals
	// whose graphs reuse the same step ids.
	other := goal.Goal{ID: "other", Person: "person", Agent: "agent", Conversation: "conversation", Summary: "Prepare a workshop", Done: "Workshop ready", NoServiceReason: "plan", Scope: goal.GoalScope{Repositories: []string{"repo"}, Includes: "workshop"}, Criteria: []goal.Criterion{{ID: "outcome", Statement: "Workshop ready", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "test", Target: "repo"}}}}
	if err = engine.Admit(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	for revision, name := range []string{"Draft notes", "Revise notes"} {
		if _, err = engine.SubmitStepGraph(t.Context(), other.ID, "person", "agent", "conversation", uint64(revision), []goal.Step{step("stay", name)}); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		batch, err := Goals(t.Context(), tx, "person", map[string]string{"agent": "Agent"}, nil)
		if err != nil {
			return err
		}
		if len(batch) != 2 {
			t.Fatalf("missing goals: %+v", batch)
		}
		for _, unit := range batch {
			individual, err := GoalStages(t.Context(), tx, unit.ID, "person", design.English)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(unit.Stages, individual) {
				t.Errorf("%s overview differs from individual history", unit.ID)
			}
			if unit.ID == other.ID && unit.Stages[0].Label != "Revise notes" {
				t.Errorf("revision lost or mixed: %+v", unit.Stages)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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

// TestPausedGoalLineAndRankOrder checks the two values the person's commands
// change on a line: a paused goal says so in Now, and goal lines follow rank.
func TestPausedGoalLineAndRankOrder(t *testing.T) {
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
	if _, _, err = engine.Command(t.Context(), goal.GoalCommand{ID: "pause-g", Goal: "g", Person: "person", Command: goal.CommandPause}); err != nil {
		t.Fatal(err)
	}
	read := func() []Entry {
		t.Helper()
		var units []Unit
		if err := db.Read(t.Context(), func(tx *canon.Tx) error {
			units, err = Goals(t.Context(), tx, "person", map[string]string{"agent": "Agent"}, func(string) string { return "Prepare the trip" })
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return Compute(units)
	}
	lines := read()
	if len(lines) != 1 {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[0].Line.Now != design.Text(design.English, "progress.paused") {
		t.Fatalf("now = %q, want the paused word", lines[0].Line.Now)
	}
	// The pause is also a typed field, so a client offers resume without
	// reading the word it localized.
	if !lines[0].Line.Paused {
		t.Fatal("a paused goal's line does not carry its pause")
	}
	// Two ranked goals of one person follow rank, whatever their ids say.
	second := goal.Goal{ID: "a", Person: "person", Agent: "agent", Conversation: "conversation", Summary: "Book the venue", Done: "The venue is booked", NoServiceReason: "plan", Scope: goal.GoalScope{Repositories: []string{"repo"}, Includes: "plan.md"}, Criteria: []goal.Criterion{{ID: "outcome", Statement: "The venue is booked", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "repo"}}}}
	if err = engine.Admit(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if lines = read(); len(lines) != 2 || lines[0].Ref != "g" || lines[1].Ref != "a" {
		t.Fatalf("rank order = %+v, want the first-admitted goal first", lines)
	}
	if _, _, err = engine.Command(t.Context(), goal.GoalCommand{ID: "down", Goal: "g", Person: "person", Command: goal.CommandRankDown}); err != nil {
		t.Fatal(err)
	}
	if lines = read(); len(lines) != 2 || lines[0].Ref != "a" || lines[1].Ref != "g" {
		t.Fatalf("order after the move = %+v", lines)
	}
	// A stopped goal leaves the lines.
	if _, _, err = engine.Command(t.Context(), goal.GoalCommand{ID: "stop-a", Goal: "a", Person: "person", Command: goal.CommandStop, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if lines = read(); len(lines) != 1 || lines[0].Ref != "g" {
		t.Fatalf("lines after the stop = %+v", lines)
	}
}
