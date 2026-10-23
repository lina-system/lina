// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

// stepSession builds a step that reached a stage with a recorded session.
func stepSession(id string, stage TaskStage) Task {
	return Task{ID: id, Session: "action-" + id, ThreadHandle: "thread-" + id, Stage: stage}
}

func TestLiveSessionsFollowTheFrontier(t *testing.T) {
	g := Goal{ID: "g", State: GoalRunning, Tasks: []Task{
		stepSession("building", TaskBuilding),
		stepSession("delivered", TaskDelivered),
		stepSession("no-receipt", TaskNoReceipt),
		stepSession("merged", TaskMerged),
		stepSession("done", TaskDone),
		stepSession("waiting", TaskNeedsPerson),
		stepSession("ready", TaskReady),
	}}
	actions := []Action{{ID: "action-building", GoalID: "g", State: "running"}}
	live := LiveSessions(g, actions)
	for _, want := range []string{"action-building", "action-delivered", "action-no-receipt"} {
		if !live[want] {
			t.Errorf("in-progress step %s has no live session: %+v", want, live)
		}
	}
	for _, want := range []string{"action-merged", "action-done", "action-waiting", "action-ready"} {
		if live[want] {
			t.Errorf("landed or waiting step %s kept a session", want)
		}
	}
	// The turn of a building step ended without a delivery: no process remains.
	settled := LiveSessions(g, []Action{{ID: "action-building", GoalID: "g", State: "finished"}})
	if settled["action-building"] {
		t.Fatalf("a settled turn kept its session: %+v", settled)
	}
	if LiveSessions(g, nil)["action-building"] {
		t.Fatal("a missing action kept its session")
	}
}

func TestLiveSessionsKeepOneRepairAndOneRecovery(t *testing.T) {
	repair := stepSession("repair", TaskConflicted)
	recovered := stepSession("recovered", TaskWorkerLost)
	recovered.EffectsSettled, recovered.Recoveries = true, 0
	spent := stepSession("spent", TaskWorkerLost)
	spent.EffectsSettled, spent.Recoveries = true, 1
	unsettled := stepSession("unsettled", TaskWorkerLost)
	live := LiveSessions(Goal{ID: "g", State: GoalRunning, Tasks: []Task{repair, recovered, spent, unsettled}}, nil)
	if !live["action-repair"] || !live["action-recovered"] {
		t.Fatalf("allowed repair or recovery lost its session: %+v", live)
	}
	if live["action-spent"] || live["action-unsettled"] {
		t.Fatalf("a step that cannot continue kept its session: %+v", live)
	}
	// A second conflict spends the one repair allowance.
	repair.Conflicts = 1
	if live := LiveSessions(Goal{ID: "g", State: GoalRunning, Tasks: []Task{repair}}, nil); len(live) != 0 {
		t.Fatalf("spent repair kept a session: %+v", live)
	}
}

func TestLiveSessionsEndWithTheGoal(t *testing.T) {
	for _, state := range []GoalState{GoalDone, GoalStopped} {
		g := Goal{ID: "g", State: state, Tasks: []Task{stepSession("delivered", TaskDelivered)}}
		if live := LiveSessions(g, []Action{{ID: "action-delivered", GoalID: "g", State: "running"}}); len(live) != 0 {
			t.Fatalf("%s goal kept a session: %+v", state, live)
		}
	}
}

func TestLiveSessionsAreDeterministic(t *testing.T) {
	g := Goal{ID: "g", State: GoalRunning, Tasks: []Task{
		stepSession("a", TaskBuilding), stepSession("b", TaskDelivered), stepSession("c", TaskMerged),
	}}
	actions := []Action{{ID: "action-a", GoalID: "g", State: "running"}}
	first, second := LiveSessions(g, actions), LiveSessions(g, actions)
	if len(first) != len(second) {
		t.Fatalf("two derivations differ: %+v %+v", first, second)
	}
	for id := range first {
		if !second[id] {
			t.Fatalf("two derivations differ: %+v %+v", first, second)
		}
	}
}

func TestEngineReapsSessionsOutsideTheFrontier(t *testing.T) {
	live := worker.NewLive()
	ends := map[string]int{}
	for _, id := range []string{"action-a", "action-b"} {
		closer := id
		if err := live.Adopt(worker.Session{ID: id, Thread: worker.Thread{ID: "thread-" + id, TaskID: "task-" + id}}, &reapAdapter{events: make(chan worker.Event)}, func(context.Context) error {
			ends[closer]++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{cfg: Config{Sessions: live}}
	s := State{Goals: []Goal{{ID: "g", State: GoalRunning, Tasks: []Task{stepSession("a", TaskDelivered)}}}}
	if err := e.reapSessions(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if ends["action-a"] != 0 || ends["action-b"] != 1 || live.Count() != 1 {
		t.Fatalf("reap did not follow the frontier: %+v live=%d", ends, live.Count())
	}
}

func TestReapWithoutRegistryKeepsEverything(t *testing.T) {
	e := &Engine{}
	if err := e.reapSessions(t.Context(), State{}); err != nil {
		t.Fatal(err)
	}
}

type reapAdapter struct{ events chan worker.Event }

func (*reapAdapter) Name() string                            { return "stub" }
func (*reapAdapter) Version(context.Context) (string, error) { return "stub-1", nil }
func (*reapAdapter) Capabilities() worker.CapabilitySet      { return nil }
func (*reapAdapter) Start(context.Context, lease.Lease, worker.StartSpec) (worker.Thread, error) {
	return worker.Thread{}, errors.New("not startable")
}
func (*reapAdapter) Input(context.Context, worker.Thread, worker.Input) error {
	return errors.New("not startable")
}
func (*reapAdapter) Interrupt(context.Context, worker.Thread, worker.Reason) error {
	return errors.New("not startable")
}
func (a *reapAdapter) Events() <-chan worker.Event { return a.events }
func (*reapAdapter) Answer(context.Context, worker.Thread, worker.Answer) error {
	return errors.New("not startable")
}
func (*reapAdapter) Quota(context.Context) ([]provider.QuotaRow, error) { return nil, nil }
func (*reapAdapter) Snapshot(context.Context, worker.Thread) (worker.ThreadSnapshot, error) {
	return worker.ThreadSnapshot{}, errors.New("not startable")
}
