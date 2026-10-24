// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
)

type panelWorkerProbe struct {
	worker.Adapter
	events chan worker.Event
	state  worker.ThreadState
}

func (p *panelWorkerProbe) Events() <-chan worker.Event { return p.events }
func (p *panelWorkerProbe) Snapshot(_ context.Context, thread worker.Thread) (worker.ThreadSnapshot, error) {
	return worker.ThreadSnapshot{Thread: thread, State: p.state, Worker: "codex"}, nil
}

func TestWorkPanelCardStagesAndLiveWorkers(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	g := goal.Goal{ID: "panel-goal", Person: r.person, Agent: r.agent, State: goal.GoalRunning,
		StepGraph: &goal.StepGraph{GoalID: "panel-goal", Revision: 1, Steps: []goal.Step{{ID: "draft", Name: "Draft"}, {ID: "check", Name: "Check", DependsOn: []string{"draft"}}}},
		Tasks:     []goal.Task{{ID: "draft", Stage: goal.TaskBuilding, Session: "draft-session"}, {ID: "check", Stage: goal.TaskReady}}}
	card := work.Card{Task: work.Task{ID: "panel-card", GoalID: g.ID, PersonID: r.person, AgentID: r.agent, Title: "Prepare workshop"}, State: "input_needed"}
	save := func() {
		t.Helper()
		if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
			raw, _ := json.Marshal(g)
			if _, err := tx.ExecContext(t.Context(), "INSERT OR REPLACE INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw); err != nil {
				return err
			}
			raw, _ = json.Marshal(card)
			_, err := tx.ExecContext(t.Context(), "INSERT OR REPLACE INTO work_cards(task_id,record) VALUES(?,?)", card.Task.ID, raw)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	r.liveWorkers = worker.NewLive()
	probe := &panelWorkerProbe{events: make(chan worker.Event), state: worker.Active}
	if err := r.liveWorkers.Adopt(worker.Session{ID: "draft-session", Worker: "codex", Thread: worker.Thread{ID: "private-thread", TurnID: "turn"}}, probe, func(context.Context) error { close(probe.events); return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.liveWorkers.Close(context.Background()) })
	read := func() map[string]any {
		t.Helper()
		p, err := r.ownerSnapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ref := card.Task.ID
		if card.Task.GoalID != "" {
			if g.Agent == r.agent {
				ref = g.ID
			}
			if p.CardOrder == nil || len(*p.CardOrder) != 1 || (*p.CardOrder)[0] != ref {
				t.Fatalf("managed work card order = %+v, want %s", p.CardOrder, ref)
			}
		}
		for _, row := range *p.Rows {
			if row.Ref != ref {
				continue
			}
			raw, _ := json.Marshal(row)
			if strings.Contains(string(raw), "private-thread") || strings.Contains(string(raw), "draft-session") {
				t.Fatal("session identity leaked")
			}
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			return value
		}
		t.Fatal("work card missing")
		return nil
	}
	row := read()
	line := row["line"].(map[string]any)
	stages, ok := line["stages"].([]any)
	if !ok || len(stages) != 3 {
		t.Fatalf("card has no Core stage list: %+v", row)
	}
	step := stages[0].(map[string]any)["steps"].([]any)[0].(map[string]any)
	workers, ok := step["workers"].([]any)
	if !ok || len(workers) != 1 || workers[0].(map[string]any)["name"] != "Codex" {
		t.Fatalf("live worker absent: %+v", step)
	}
	if _, ok := row["stage_refs"]; !ok {
		t.Fatal("step selection refs absent")
	}
	if _, ok := step["ref"]; ok {
		t.Fatal("identity entered display line")
	}
	if err := r.liveWorkers.End(t.Context(), "draft-session"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(read())
	if strings.Contains(string(raw), `"workers"`) {
		t.Fatal("ended worker remained visible")
	}
	// Active dispatches still belong to the decision bridge; only delivered
	// sessions have transferred to worker.Live. Both must reach the panel.
	entry := &workerSession{goalSession: "draft-session", goalPerson: r.person, goalAgent: r.agent, goalWorker: "codex", origin: r, adapter: probe, thread: worker.Thread{ID: "active-thread", TurnID: "active-turn"}, done: make(chan struct{})}
	r.workers.mu.Lock()
	r.workers.sessions[entry.thread.ID] = entry
	r.workers.mu.Unlock()
	t.Cleanup(func() {
		r.workers.mu.Lock()
		delete(r.workers.sessions, entry.thread.ID)
		r.workers.mu.Unlock()
		entry.stageSnapshot.Close()
		close(entry.done)
	})
	raw, _ = json.Marshal(read())
	if !strings.Contains(string(raw), `"workers"`) {
		t.Fatal("active dispatch was hidden until its turn finished")
	}
	g.Agent = "foreign-agent"
	save()
	if _, ok := read()["line"].(map[string]any)["stages"]; ok {
		t.Fatal("foreign agent graph attached to card")
	}
	// Real goal dispatch cards are managed assignments without Task.GoalID.
	// Their trusted action/owner records are the link, not their display text.
	g.Agent = r.agent
	card.Task.GoalID = ""
	save()
	d := dispatch.Record{ID: card.Task.ID, Goal: true, GoalAction: "draft-session", Owner: r.agent, Origin: dispatch.Origin{Person: r.person, Agent: r.agent}}
	a := goal.Action{ID: d.GoalAction, GoalID: g.ID, TaskID: "draft", Kind: goal.Assign}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		raw, _ := json.Marshal(d)
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)", d.ID, "turn", "fixture", "running", raw); err != nil {
			return err
		}
		raw, _ = json.Marshal(a)
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('action',?,1,?)", a.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := read()["line"].(map[string]any)["stages"]; !ok {
		t.Fatal("production dispatch card has no stage list")
	}
	manager, err := r.agents.Create(t.Context(), agents.Create{Name: "Manager", PersonID: r.person, Preset: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	card.Task.AgentID, d.Owner = manager.ID, manager.ID
	saveDispatch := func() {
		t.Helper()
		if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
			raw, _ := json.Marshal(d)
			_, err := tx.ExecContext(t.Context(), "UPDATE worker_dispatch SET record=? WHERE id=?", raw, d.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	saveDispatch()
	raw, _ = json.Marshal(read())
	if !strings.Contains(string(raw), `"workers"`) {
		t.Fatal("manager persona hid the original goal's worker")
	}
	d.Origin.Person = "someone-else"
	saveDispatch()
	if _, ok := read()["line"].(map[string]any)["stages"]; ok {
		t.Fatal("foreign dispatch origin linked the graph")
	}
	d.Origin.Person = r.person
	saveDispatch()
	g.State = goal.GoalDone
	for i := range g.Tasks {
		g.Tasks[i].Stage = goal.TaskDone
	}
	save()
	closed := read()["line"].(map[string]any)["stages"].([]any)
	if len(closed) != 1 || closed[0].(map[string]any)["kind"] != "done" {
		t.Fatalf("closed goal stages: %+v", closed)
	}
}

// A worker RPC may never answer. Canonical progress must still publish while
// that RPC is held, so this probe releases only after publication is checked.
type panelBlockedProbe struct {
	worker.Adapter
	events  chan worker.Event
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *panelBlockedProbe) Events() <-chan worker.Event { return p.events }
func (p *panelBlockedProbe) Snapshot(_ context.Context, thread worker.Thread) (worker.ThreadSnapshot, error) {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return worker.ThreadSnapshot{Thread: thread, State: worker.Active}, nil
}

func TestWorkPanelPublishesWhileWorkerReadIsBlocked(t *testing.T) {
	t.Run("retained", func(t *testing.T) { testPanelBlockedObservation(t, false) })
	t.Run("active", func(t *testing.T) { testPanelBlockedObservation(t, true) })
}

func testPanelBlockedObservation(t *testing.T, active bool) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	probe := &panelBlockedProbe{events: make(chan worker.Event), entered: make(chan struct{}), release: make(chan struct{})}
	if active {
		entry := &workerSession{goalSession: "held", goalPerson: r.person, goalAgent: r.agent, goalWorker: "codex", origin: r, adapter: probe, thread: worker.Thread{ID: "held-thread", TurnID: "turn"}, done: make(chan struct{})}
		r.workers.mu.Lock()
		r.workers.sessions[entry.thread.ID] = entry
		r.workers.mu.Unlock()
		t.Cleanup(func() {
			close(probe.release)
			entry.stageSnapshot.Close()
			r.workers.mu.Lock()
			delete(r.workers.sessions, entry.thread.ID)
			r.workers.mu.Unlock()
			close(entry.done)
		})
	} else {
		r.liveWorkers = worker.NewLive()
		if err := r.liveWorkers.Adopt(worker.Session{ID: "held", Worker: "codex", Thread: worker.Thread{ID: "held-thread", TurnID: "turn"}}, probe, func(context.Context) error { close(probe.events); return nil }); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { close(probe.release); _ = r.liveWorkers.Close(context.Background()) })
	}
	g := goal.Goal{ID: "held-goal", Person: r.person, Agent: r.agent, State: goal.GoalRunning,
		StepGraph: &goal.StepGraph{GoalID: "held-goal", Revision: 1, Steps: []goal.Step{{ID: "held-step", Name: "Work"}, {ID: "ask-step", Name: "Choose"}}},
		Tasks:     []goal.Task{{ID: "held-step", Stage: goal.TaskBuilding, Session: "held"}, {ID: "ask-step", Stage: goal.TaskReady}}}
	save := func() {
		t.Helper()
		if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
			raw, _ := json.Marshal(g)
			_, err := tx.ExecContext(t.Context(), "INSERT OR REPLACE INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	published := make(chan error, 1)
	go func() { published <- r.publishOwnerViews(t.Context()) }()
	<-probe.entered
	assertPublished := func() {
		t.Helper()
		select {
		case err := <-published:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("canonical progress publication waited for worker I/O")
		}
	}
	assertPublished()
	g.Tasks[1].Stage = goal.TaskNeedsPerson
	save()
	go func() { published <- r.publishOwnerViews(t.Context()) }()
	assertPublished()
	initial, sub, err := r.topics.Subscribe("progress", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var update protocol.ProgressUpdate
	if len(initial.Snapshot.Items) != 1 {
		t.Fatal("missing progress")
	}
	if err := json.Unmarshal(initial.Snapshot.Items[0].Data, &update); err != nil {
		t.Fatal(err)
	}
	if update.Counts == nil || update.Counts.Waiting != "1" {
		t.Fatalf("person wait was not published: %+v", update.Counts)
	}
	row := (*update.Rows)[0]
	for _, step := range *(*row.Line.Stages)[0].Steps {
		if step.Name != "Work" {
			continue
		}
		if step.Workers == nil || len(*step.Workers) != 1 || (*step.Workers)[0].State != protocol.RunningStateUnknown {
			t.Fatalf("pending observation must remain unknown: %+v", step.Workers)
		}
		return
	}
	t.Fatal("working step was not published")
}
