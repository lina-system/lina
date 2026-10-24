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

// A resent message keeps the command it committed even when the retry reaches
// Core at a later second; the ledger holds one event and the goal keeps its
// state.
func TestResentCommandCommitsOnceAcrossClockTicks(t *testing.T) {
	_, db := testJournal(t)
	now := int64(100)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(now, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("a"))
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	command := GoalCommand{ID: "retry-pause", Goal: "g", Person: "person", Command: CommandPause}
	if _, changed, err := e.Command(t.Context(), command); err != nil || !changed {
		t.Fatalf("pause = %v %v", changed, err)
	}
	now = 400
	if _, changed, err := e.Command(t.Context(), command); err != nil || !changed {
		t.Fatalf("resend = %v %v, want the committed command", changed, err)
	}
	if count := commandEvents(t, db, "goal.command"); count != 1 {
		t.Fatalf("resent command recorded %d events, want 1", count)
	}
	if got := goalByID(t, e, "g"); !got.Paused {
		t.Fatal("resend changed the paused goal")
	}
	// A different command under the same identity is refused, not applied.
	if _, _, err := e.Command(t.Context(), GoalCommand{ID: "retry-pause", Goal: "g", Person: "person", Command: CommandStop, Confirmed: true}); err == nil {
		t.Fatal("a second command reused one message identity")
	}
}

// A confirmed stop is final: a claimed planning action that settles afterwards
// keeps its receipt and never reopens the goal.
func TestStopIsFinalAgainstALatePlanReport(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("a"))
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	// A person's correction makes the goal wait for a manager plan.
	admitted := goalByID(t, e, "g")
	if _, err = e.Revise(t.Context(), "g", admitted.RecordRevision, "person", "agent", Definition{Outcome: "complete the project again", Done: "all changes run", Scope: admitted.Scope, Criteria: admitted.Criteria}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan := ""
	for _, a := range s.Actions {
		if a.GoalID == "g" && a.Kind == PlanGoal {
			plan = a.ID
		}
	}
	if plan == "" {
		t.Fatal("no queued correction plan")
	}
	if _, err = e.Claim(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.Command(t.Context(), GoalCommand{ID: "stop-g", Goal: "g", Person: "person", Command: CommandStop, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	// The manager's late plan would satisfy the correction.
	if err = e.Report(t.Context(), plan, 1, Observation{Status: OutcomeSucceeded, EffectsSettled: true, Tasks: []Task{readyTask("a")}}); err != nil {
		t.Fatalf("late plan report: %v", err)
	}
	if got := goalByID(t, e, "g"); got.State != GoalStopped || len(got.Waits) != 0 {
		t.Fatalf("late plan reopened the stopped goal: %+v", got)
	}
	after, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range after.Actions {
		if a.GoalID == "g" && a.State != "finished" {
			t.Fatalf("stopped goal still holds action %q in state %q", a.ID, a.State)
		}
	}
}

// Pausing a goal also stops its automatic worker recovery: only the current
// turn may finish.
func TestPausedGoalRefusesRecoveryAuthorization(t *testing.T) {
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
	assign := ""
	for _, a := range s.Actions {
		if a.GoalID == "first" && a.Kind == Assign {
			assign = a.ID
		}
	}
	if assign == "" {
		t.Fatal("no queued assignment for the first goal")
	}
	if _, err = e.Claim(t.Context(), assign); err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.Command(t.Context(), GoalCommand{ID: "pause-first", Goal: "first", Person: "person", Command: CommandPause}); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.CanContinue(t.Context(), assign); err != nil || ok {
		t.Fatalf("recovery authorization = %v %v, want refusal for the paused goal", ok, err)
	}
}

// A stopped goal's settled result no longer holds its write area, so another
// goal touching the same area starts on the next pass. An unsettled effect
// keeps its lease until it is read.
func TestStoppedGoalReleasesSettledArea(t *testing.T) {
	stopped := readyTask("a")
	stopped.Stage = TaskDelivered
	stopped.EffectsSettled = true
	first := oneGoal(stopped)
	first.ID, first.CreatedAt, first.State = "first", 100, GoalStopped
	other := readyTask("b")
	other.Area = stopped.Area
	second := oneGoal(other)
	second.ID, second.CreatedAt = "second", 101
	plan, err := Derive(State{Goals: []Goal{first, second}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := planAssignments(plan); len(got) != 1 || got[0] != "b" {
		t.Fatalf("derived %v, want the other goal's step", got)
	}
	for _, w := range plan.Waits {
		if w.Reason == "area" {
			t.Fatalf("settled result of a stopped goal still holds its area: %+v", plan.Waits)
		}
	}
	// An effect whose outcome is still unknown keeps the lease.
	stopped.EffectsSettled = false
	first.Tasks[0] = stopped
	plan, err = Derive(State{Goals: []Goal{first, second}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := planAssignments(plan); len(got) != 0 {
		t.Fatalf("unsettled effect released its area: %v", got)
	}
}

// An uncertain execution of a stopped goal stays observable: its reservation is
// released only when the outcome is actually settled, never by the stop alone.
func TestStoppedGoalKeepsUncertainActionObservable(t *testing.T) {
	for _, status := range []Outcome{OutcomeRunning, OutcomeUnknown, OutcomeNeedsPerson} {
		t.Run(string(status), func(t *testing.T) {
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
			assign := ""
			for _, a := range s.Actions {
				if a.GoalID == "first" && a.Kind == Assign {
					assign = a.ID
				}
			}
			if assign == "" {
				t.Fatal("no queued assignment for the first goal")
			}
			if _, err = e.Claim(t.Context(), assign); err != nil {
				t.Fatal(err)
			}
			if _, _, err = e.Command(t.Context(), GoalCommand{ID: "stop-first", Goal: "first", Person: "person", Command: CommandStop, Confirmed: true}); err != nil {
				t.Fatal(err)
			}
			if err = e.Report(t.Context(), assign, 1, Observation{Status: status, Handle: "live-worker"}); err != nil {
				t.Fatalf("uncertain report: %v", err)
			}
			after, err := e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			state := ""
			for _, a := range after.Actions {
				if a.ID == assign {
					state = a.State
					if !reserves(a) || a.Handle != "live-worker" {
						t.Fatalf("lost reservation or observation handle: %+v", a)
					}
				}
			}
			if state == "finished" {
				t.Fatal("an uncertain execution was marked finished by the stop")
			}
			if got := goalByID(t, e, "first"); got.State != GoalStopped {
				t.Fatalf("goal state = %q, want stopped", got.State)
			}
			// Advance the controlled clock to the sweep and let the driver settle it.
			e.cfg.Now = func() time.Time { return time.Unix(100, 0).Add(SafetySweep) }
			reads := 0
			e.cfg.Driver = &fixtureDriver{run: func(Action) (Observation, error) {
				t.Error("started new execution after stop")
				return Observation{}, ErrInvalid
			}, observe: func(a Action) (Observation, error) {
				reads++
				if a.ID != assign || a.Handle != "live-worker" {
					t.Errorf("wrong effect observed: %+v", a)
				}
				return Observation{Status: OutcomeSucceeded, EffectsSettled: true}, nil
			}}
			// Keep the other goal paused so this pass only reads the stopped effect.
			if _, _, err = e.Command(t.Context(), GoalCommand{ID: "pause-second", Goal: "second", Person: "person", Command: CommandPause}); err != nil {
				t.Fatal(err)
			}
			authorizationStep(t, e)
			if reads != 1 {
				t.Fatalf("observed %d effects, want 1", reads)
			}
			final, err := e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range final.Actions {
				if a.ID == assign && a.State != "finished" {
					t.Fatalf("settled action state = %q", a.State)
				}
			}
			if got := Slots(readyNode("n"), final.Actions, readyTask("z").Resources, time.Unix(100, 0)); got < 1 {
				t.Fatalf("settled action still holds the slot: %d free", got)
			}
		})
	}
}

// A non-worker report for a stopped goal never clears the worker settlement the
// delivered task already recorded, so its write area is released.
func TestStoppedGoalPreservesWorkerSettlement(t *testing.T) {
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
	assign := ""
	for _, a := range s.Actions {
		if a.GoalID == "first" && a.Kind == Assign {
			assign = a.ID
		}
	}
	if assign == "" {
		t.Fatal("no queued assignment for the first goal")
	}
	if _, err = e.Claim(t.Context(), assign); err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), assign, 1, Observation{Status: OutcomeSucceeded, EffectsSettled: true, Head: "head", Proof: Proof{Revision: "draft", Ref: "receipt", Passed: true}}); err != nil {
		t.Fatal(err)
	}
	delivered, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stage, settled := TaskReady, false
	for _, g := range delivered.Goals {
		if g.ID == "first" {
			stage, settled = g.Tasks[0].Stage, g.Tasks[0].EffectsSettled
		}
	}
	if !settled {
		t.Fatal("the delivered task did not record its settlement")
	}
	// The delivered task derives its PR inspection while the goal is still
	// active; the person stops the goal while that inspection is in flight.
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queued, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	inspect := ""
	for _, a := range queued.Actions {
		if a.GoalID == "first" && a.Kind == InspectPR && a.State == "queued" {
			inspect = a.ID
		}
	}
	if inspect == "" {
		t.Fatal("no queued inspection for the delivered task")
	}
	if _, err = e.Claim(t.Context(), inspect); err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.Command(t.Context(), GoalCommand{ID: "stop-first", Goal: "first", Person: "person", Command: CommandStop, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	// A stopped goal derives no merge; the late inspection result carries no
	// worker settlement field, so the delivered task keeps its own.
	if err = e.Report(t.Context(), inspect, 1, Observation{Status: OutcomeSucceeded, Head: "head"}); err != nil {
		t.Fatalf("late report: %v", err)
	}
	after, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range after.Actions {
		if a.ID == inspect && a.State != "finished" {
			t.Fatalf("completed inspection still needs observation: %+v", a)
		}
	}
	for _, g := range after.Goals {
		if g.ID == "first" && !g.Tasks[0].EffectsSettled {
			t.Fatalf("late report cleared the settlement of stage %q", stage)
		}
	}
}

// An authorization refusal that arrives after a stop retires the award instead
// of holding its reservation for a goal that can no longer run it.
func TestStopRetiresALateAuthorizationRefusal(t *testing.T) {
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
	var assign Action
	for _, a := range s.Actions {
		if a.GoalID == "first" && a.Kind == Assign {
			assign = a
		}
	}
	if assign.ID == "" {
		t.Fatal("no queued assignment for the first goal")
	}
	claimed, err := e.Claim(t.Context(), assign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.Command(t.Context(), GoalCommand{ID: "stop-first", Goal: "first", Person: "person", Command: CommandStop, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if err = e.awaitAuthorization(t.Context(), claimed); err != nil {
		t.Fatalf("authorization wait: %v", err)
	}
	after, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range after.Actions {
		if a.ID == assign.ID && a.State != "finished" {
			t.Fatalf("authorization refusal kept state %q after the stop", a.State)
		}
	}
	if got := Slots(readyNode("n"), after.Actions, readyTask("z").Resources, time.Unix(100, 0)); got < 1 {
		t.Fatalf("authorization refusal still holds the slot: %d free", got)
	}
	if got := goalByID(t, e, "first"); got.State != GoalStopped {
		t.Fatalf("goal state = %q, want stopped", got.State)
	}
}

// A replacement read queued before Stop still observes the original effect.
func TestStopAllowsQueuedReconciliationRead(t *testing.T) {
	e, original := claimedAssignment(t)
	if err := e.Signal(t.Context(), Signal{ID: "new-input", ActionID: original.ID}); err != nil {
		t.Fatal(err)
	}
	if err := e.Report(t.Context(), original.ID, e.cfg.Epoch, Observation{Status: OutcomeRunning, Handle: "live"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	readID := ""
	for _, a := range s.Actions {
		if a.ObserveActionID == original.ID && a.State == "queued" {
			readID = a.ID
		}
	}
	if readID == "" {
		t.Fatal("no queued replacement read")
	}
	if _, _, err = e.Command(t.Context(), GoalCommand{ID: "stop", Goal: original.GoalID, Person: "person", Command: CommandStop, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	e.cfg.Driver = &fixtureDriver{run: func(Action) (Observation, error) {
		t.Error("executed work after stop")
		return Observation{}, ErrInvalid
	}, observe: func(a Action) (Observation, error) {
		reads++
		if a.ID != original.ID || a.Handle != "live" {
			t.Errorf("wrong effect observed: %+v", a)
		}
		return Observation{Status: OutcomeSucceeded, EffectsSettled: true}, nil
	}}
	after := authorizationStep(t, e)
	if reads != 1 {
		t.Fatalf("observed %d effects, want 1", reads)
	}
	for _, a := range after.Actions {
		if a.ID == readID && (a.State != "finished" || reserves(a)) {
			t.Fatalf("replacement did not settle: %+v", a)
		}
	}
	g := goalByID(t, e, original.GoalID)
	if g.State != GoalStopped || !g.Tasks[0].EffectsSettled {
		t.Fatalf("stopped goal did not settle: %+v", g)
	}
}
