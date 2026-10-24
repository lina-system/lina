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
)

// TestQueuedGoalLineShowsReasonAndPlace checks that a goal waiting for a run
// slot says why and which place it holds, and that its place follows rank.
func TestQueuedGoalLineShowsReasonAndPlace(t *testing.T) {
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
	now := time.Unix(100, 0)
	engine, err := goal.NewEngine(goal.Config{DB: db, Records: stageDefinitions{repo}, Epoch: 1, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	admitGoal(t, engine)
	second := goal.Goal{ID: "b", Person: "person", Agent: "agent", Conversation: "conversation", Summary: "Book the venue", Done: "The venue is booked", NoServiceReason: "plan", Scope: goal.GoalScope{Repositories: []string{"repo"}, Includes: "plan.md"}, Criteria: []goal.Criterion{{ID: "outcome", Statement: "The venue is booked", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "acceptance", Target: "repo"}}}}
	if err = engine.Admit(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	step := func(id, name string) goal.Step {
		return goal.Step{ID: id, Name: name, Done: "Produce " + name, Repository: "repo", Base: "dev", Area: id + ".txt", Worker: "antigravity", Resources: goal.Resources{CPU: 0.01, MemoryMB: 1, DiskMB: 1}}
	}
	for _, g := range []struct{ id, step, name string }{{"g", "first", "숙소 고르기"}, {"b", "second", "장소 비교"}} {
		if _, err = engine.SubmitStepGraph(t.Context(), g.id, "person", "agent", "conversation", 0, []goal.Step{step(g.step, g.name)}); err != nil {
			t.Fatal(err)
		}
	}
	// One host can run many steps; the person's limit of one keeps the other
	// goal waiting for a slot.
	host := goal.Node{ID: "main", Connected: true, ObservedAt: now.Unix(), CPUCores: 4, CPULimit: 4, AvailableMemoryMB: 4096, MemoryLimitMB: 4096, AvailableDiskMB: 4096, DiskLimitMB: 4096, Workers: map[string]bool{"antigravity": true}, QuotaReset: map[string]int64{"antigravity": 0}}
	if err = engine.NodeChanged(t.Context(), host); err != nil {
		t.Fatal(err)
	}
	if _, _, err = engine.SetRunLimit(t.Context(), "person", goal.RunLimit{Value: 1}, "limit-one"); err != nil {
		t.Fatal(err)
	}
	if err = engine.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	lines := func() []Entry {
		t.Helper()
		var units []Unit
		if err := db.Read(t.Context(), func(tx *canon.Tx) error {
			units, err = Goals(t.Context(), tx, "person", map[string]string{"agent": "Agent"}, func(id string) string {
				return map[string]string{"g": "Prepare the trip", "b": "Book the venue"}[id]
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return Compute(units)
	}
	read := lines()
	if len(read) != 2 {
		t.Fatalf("lines = %+v", read)
	}
	if read[0].Ref != "g" || read[1].Ref != "b" {
		t.Fatalf("rank order = %+v", read)
	}
	if got, want := read[1].Line.Now, "Waiting for a slot · #1"; got != want {
		t.Fatalf("queued Now = %q, want %q", got, want)
	}
	if read[0].Line.Now == read[1].Line.Now {
		t.Fatalf("the running goal shows a queue place: %+v", read)
	}
	// A person's move swaps the place, and the running goal changes with it.
	if _, _, err = engine.Command(t.Context(), goal.GoalCommand{ID: "up", Goal: "b", Person: "person", Command: goal.CommandRankUp}); err != nil {
		t.Fatal(err)
	}
	if err = engine.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if read = lines(); read[0].Ref != "b" || read[0].Line.Now != "Waiting for a slot · #1" {
		t.Fatalf("moved queue = %+v", read)
	}
	// A quota-blocked report is the reason the automatic limit fell.
	if _, _, err = engine.SetRunLimit(t.Context(), "person", goal.RunLimit{Automatic: true}, "limit-auto"); err != nil {
		t.Fatal(err)
	}
	blocked := host
	blocked.QuotaReset = map[string]int64{"antigravity": now.Unix() + 60}
	if err = engine.NodeChanged(t.Context(), blocked); err != nil {
		t.Fatal(err)
	}
	if err = engine.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	record, err := engine.CurrentRunLimit(t.Context(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if !record.Automatic || record.Current != 1 || record.Reason != goal.ReasonQuota {
		t.Fatalf("limit record = %+v", record)
	}
}
