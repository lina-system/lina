// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// twoGoalEngine admits two ready goals of one person, oldest first.
func twoGoalEngine(t *testing.T) (*Engine, *testStore) {
	t.Helper()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	first, second := oneGoal(readyTask("a")), oneGoal(readyTask("b"))
	first.ID, first.CreatedAt = "first", 100
	second.ID, second.CreatedAt = "second", 101
	for _, g := range []Goal{first, second} {
		if err = e.Admit(t.Context(), g); err != nil {
			t.Fatal(err)
		}
	}
	return e, db
}

func goalByID(t *testing.T, e *Engine, id string) Goal {
	t.Helper()
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range s.Goals {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("goal %q missing", id)
	return Goal{}
}

// commandEvents counts the recorded commands of one goal by kind.
func commandEvents(t *testing.T, db *testStore, kind string) int {
	t.Helper()
	var count int
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_events WHERE kind=?", kind).Scan(&count)
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestAdmittedGoalsTakeTheLastRank(t *testing.T) {
	e, _ := twoGoalEngine(t)
	if first := goalByID(t, e, "first"); first.Rank != 1 {
		t.Fatalf("first rank = %d, want 1", first.Rank)
	}
	if second := goalByID(t, e, "second"); second.Rank != 2 {
		t.Fatalf("second rank = %d, want 2", second.Rank)
	}
}

func TestRankCommandReordersReadyPlacementOnTheNextDerivation(t *testing.T) {
	e, db := twoGoalEngine(t)
	if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	before, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Derive(before, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := planAssignments(plan); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("placement before the move = %v, want [a b]", got)
	}
	if _, _, err = e.Command(t.Context(), GoalCommand{ID: "cmd-rank-first-down", Goal: "first", Person: "person", Command: CommandRankDown}); err != nil {
		t.Fatal(err)
	}
	if first := goalByID(t, e, "first"); first.Rank != 2 {
		t.Fatalf("moved goal rank = %d, want 2", first.Rank)
	}
	if second := goalByID(t, e, "second"); second.Rank != 1 {
		t.Fatalf("neighbour rank = %d, want 1", second.Rank)
	}
	after, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err = Derive(after, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := planAssignments(plan); len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("placement after the move = %v, want [b a]", got)
	}
	if count := commandEvents(t, db, "goal.command"); count != 1 {
		t.Fatalf("recorded %d commands, want 1", count)
	}
	// A resent message keeps its identity and records nothing twice.
	if _, _, err = e.Command(t.Context(), GoalCommand{ID: "cmd-rank-first-down", Goal: "first", Person: "person", Command: CommandRankDown}); err != nil {
		t.Fatal(err)
	}
	if count := commandEvents(t, db, "goal.command"); count != 1 {
		t.Fatalf("resent command recorded %d events, want 1", count)
	}
}

func TestRankCommandAtTheEdgeLeavesOrder(t *testing.T) {
	e, db := twoGoalEngine(t)
	if _, changed, err := e.Command(t.Context(), GoalCommand{ID: "cmd-first-up", Goal: "first", Person: "person", Command: CommandRankUp}); err != nil || changed {
		t.Fatalf("edge rank changed=%v err=%v, want no change", changed, err)
	}
	// A choice that changes nothing records no command.
	if count := commandEvents(t, db, "goal.command"); count != 0 {
		t.Fatalf("edge rank recorded %d commands, want none", count)
	}
	if first := goalByID(t, e, "first"); first.Rank != 1 {
		t.Fatalf("top goal rank = %d, want 1", first.Rank)
	}
	if second := goalByID(t, e, "second"); second.Rank != 2 {
		t.Fatalf("second rank = %d, want 2", second.Rank)
	}
}

func TestPausedGoalGetsNoNewStepUntilResumed(t *testing.T) {
	e, db := twoGoalEngine(t)
	if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	queued := ""
	for _, a := range s.Actions {
		if a.GoalID == "first" && a.Kind == Assign {
			queued = a.ID
		}
	}
	if queued == "" {
		t.Fatal("no queued assignment for the first goal")
	}
	if _, changed, err := e.Command(t.Context(), GoalCommand{ID: "cmd-pause-first", Goal: "first", Person: "person", Command: CommandPause}); err != nil || !changed {
		t.Fatalf("pause changed=%v err=%v, want a change", changed, err)
	}
	if _, changed, err := e.Command(t.Context(), GoalCommand{ID: "cmd-pause-again", Goal: "first", Person: "person", Command: CommandPause}); err != nil || changed {
		t.Fatalf("repeated pause changed=%v err=%v, want no change", changed, err)
	}
	if g := goalByID(t, e, "first"); !g.Paused {
		t.Fatal("paused command left the goal running")
	}
	if _, err = e.Claim(t.Context(), queued); !errors.Is(err, ErrConflict) {
		t.Fatalf("claimed a paused goal's step: %v", err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	paused, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Derive(paused, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range planAssignments(plan) {
		if id == "a" {
			t.Fatalf("paused goal derived a new step: %+v", plan)
		}
	}
	if _, _, err = e.Command(t.Context(), GoalCommand{ID: "cmd-resume-first", Goal: "first", Person: "person", Command: CommandResume}); err != nil {
		t.Fatal(err)
	}
	if g := goalByID(t, e, "first"); g.Paused {
		t.Fatal("resume left the goal paused")
	}
	// The award committed before the pause kept its queue place; resume lets the
	// next pass claim it instead of deriving a second assignment for the task.
	after, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err = Derive(after, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := planAssignments(plan); len(got) != 0 {
		t.Fatalf("resume derived a second assignment for queued work: %v", got)
	}
	if _, err = e.Claim(t.Context(), queued); err != nil {
		t.Fatalf("resumed goal could not claim its queued step: %v", err)
	}
	if count := commandEvents(t, db, "goal.command"); count != 2 {
		t.Fatalf("recorded %d commands, want 2", count)
	}
}

func TestStopCommandNeedsConfirmationAndIsFinal(t *testing.T) {
	e, db := twoGoalEngine(t)
	if _, _, err := e.Command(t.Context(), GoalCommand{ID: "cmd-stop-first", Goal: "first", Person: "person", Command: CommandStop}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unconfirmed stop = %v, want ErrInvalid", err)
	}
	if g := goalByID(t, e, "first"); g.State == GoalStopped {
		t.Fatal("unconfirmed stop closed the goal")
	}
	if _, _, err := e.Command(t.Context(), GoalCommand{ID: "cmd-stop-first", Goal: "first", Person: "person", Command: CommandStop, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if g := goalByID(t, e, "first"); g.State != GoalStopped {
		t.Fatalf("stopped goal state = %q", g.State)
	}
	if _, _, err := e.Command(t.Context(), GoalCommand{ID: "cmd-pause-stopped", Goal: "first", Person: "person", Command: CommandPause}); !errors.Is(err, ErrConflict) {
		t.Fatalf("paused a stopped goal: %v", err)
	}
	if _, _, err := e.Command(t.Context(), GoalCommand{ID: "cmd-stop-first", Goal: "first", Person: "person", Command: CommandStop, Confirmed: true}); err != nil {
		t.Fatalf("replayed confirmed stop: %v", err)
	}
	// A second confirmed stop of the same goal is already its state.
	if g, _, err := e.Command(t.Context(), GoalCommand{ID: "cmd-stop-again", Goal: "first", Person: "person", Command: CommandStop, Confirmed: true}); err != nil || g.State != GoalStopped {
		t.Fatalf("repeated stop = %+v %v", g, err)
	}
	if count := commandEvents(t, db, "goal.command"); count != 1 {
		t.Fatalf("repeated stop recorded %d commands, want 1", count)
	}
	// A refusal before the transaction records nothing; only committed choices
	// are ledger commands.
	if count := commandEvents(t, db, "goal.command"); count != 1 {
		t.Fatalf("recorded %d commands, want only the confirmed stop", count)
	}
	// A refused command must not be recorded as an applied one.
	var refused int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_events WHERE id=?", "goal.command:cmd-stop-first").Scan(&refused)
	}); err != nil {
		t.Fatal(err)
	}
	if refused != 1 {
		t.Fatalf("command event rows = %d, want 1", refused)
	}
	stopped, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Derive(stopped, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range plan.Actions {
		if a.GoalID == "first" {
			t.Fatalf("stopped goal derived an action: %+v", a)
		}
	}
}

func TestCommandRefusesAnotherPerson(t *testing.T) {
	e, _ := twoGoalEngine(t)
	if _, _, err := e.Command(t.Context(), GoalCommand{ID: "cmd-other", Goal: "first", Person: "stranger", Command: CommandPause}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign command = %v, want ErrNotFound", err)
	}
	if g := goalByID(t, e, "first"); g.Paused {
		t.Fatal("foreign command changed the goal")
	}
}

func planAssignments(plan Plan) []string {
	out := []string{}
	for _, a := range plan.Actions {
		if a.Kind == Assign {
			out = append(out, a.TaskID)
		}
	}
	return out
}

// A stopped goal keeps what it produced but no undispatched award may keep a
// node slot reserved: the slot is free for other work at once.
func TestStopReleasesUndispatchedAwards(t *testing.T) {
	e, _ := twoGoalEngine(t)
	if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	queued := ""
	for _, a := range s.Actions {
		if a.GoalID == "first" && a.Kind == Assign {
			queued = a.ID
		}
	}
	if queued == "" {
		t.Fatal("no queued assignment for the first goal")
	}
	if _, _, err = e.Command(t.Context(), GoalCommand{ID: "cmd-stop-first", Goal: "first", Person: "person", Command: CommandStop, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	after, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range after.Actions {
		if a.ID == queued && a.State != "finished" {
			t.Fatalf("stopped goal kept its award %q in state %q", a.ID, a.State)
		}
	}
	if got := Slots(readyNode("n"), after.Actions, readyTask("z").Resources, time.Unix(100, 0)); got < 1 {
		t.Fatalf("stopped goal still holds the slot: %d free", got)
	}
}
