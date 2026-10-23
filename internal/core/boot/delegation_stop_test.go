// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
)

type scheduledStopProbe struct {
	workerProbe
	complete func() error
}

func (p *scheduledStopProbe) Interrupt(context.Context, worker.Thread, worker.Reason) error {
	p.interrupts++
	return p.complete()
}

func stopTask(r *conversationRuntime, id string) work.Task {
	return work.Task{ID: id, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Title: id, Done: "checked", Schedule: &work.Schedule{Group: "graph", Repository: "owner/repo",
			Areas: []string{id}, Worker: "codex", Host: "local"}}
}

func TestScheduledStopAllPreventsCompletionRelease(t *testing.T) {
	r := runRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	r.workers = &workerRuntime{sessions: map[string]*workerSession{}}
	d := &delegationRuntime{root: r}
	r.delegation = d
	t.Cleanup(func() { r.delegation = nil; r.workers = nil })
	var started []string
	d.reactor = reactor.New(r.work, reactor.Hooks{
		Availability: func(context.Context) (work.Availability, error) {
			return work.Availability{Hosts: map[string]int{"local": 1}}, nil
		},
		Start: func(_ context.Context, c work.Card) (work.Run, error) {
			started = append(started, c.Task.ID)
			thread := worker.Thread{ID: "thread:" + c.Task.ID, TaskID: c.Task.ID, TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
			probe := &scheduledStopProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}}
			probe.complete = func() error {
				return d.reactor.Completed(t.Context(), c.Task.ID, thread.ID, thread.TurnID, "interrupted")
			}
			r.workers.sessions[thread.ID] = &workerSession{scheduled: true, origin: r, adapter: probe, thread: thread, active: map[string]lease.Lease{}}
			return work.Run{ID: "run:" + c.Task.ID, TaskID: c.Task.ID, Generation: 1, Worker: "codex", NodeID: "local", ThreadID: thread.ID, TurnID: thread.TurnID}, nil
		},
	})
	if err := d.reactor.Schedule(t.Context(), []work.Task{stopTask(r, "a"), stopTask(r, "b")}); err != nil {
		t.Fatal(err)
	}
	result, err := r.stopAllRuns(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{
		MessageID: "stop-all", Payload: protocol.MultiTaskControl{Action: "stop_all", ConversationID: &r.conversation}})
	if err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 {
		t.Fatalf("stop all launched queued work: %v", started)
	}
	if p := result.Payload.(protocol.MultiTaskControl); p.Count == nil || *p.Count != "2" {
		t.Fatalf("stop did not count active and queued work once: %+v", p)
	}
	if err := r.work.Recover(t.Context(), "restart"); err != nil {
		t.Fatal(err)
	}
	if err := d.reactor.Wake(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		c, err := r.work.Get(t.Context(), id)
		if err != nil || !c.Scheduling.Closed || c.State != "stopped" || c.Scheduling.AreaHeld || c.Scheduling.CapacityHeld {
			t.Fatalf("stop lost on recovery: %+v, %v", c, err)
		}
	}
}

func TestScheduledQueuedStopIsScopedAndDurable(t *testing.T) {
	r := runRuntime(t)
	a, other, foreign := stopTask(r, "a"), stopTask(r, "other"), stopTask(r, "foreign")
	other.ConversationID = "other-conversation"
	foreign.PersonID = "someone-else"
	if err := r.work.Schedule(t.Context(), []work.Task{a, other, foreign}); err != nil {
		t.Fatal(err)
	}
	s := clientsrv.Session{PersonID: r.person}
	for _, id := range []string{"task:other:0", "task:foreign:0"} {
		if _, err := r.stopRun(t.Context(), s, protocol.Envelope{Payload: protocol.Stop{RunID: &id, ConversationID: &r.conversation}}); err == nil {
			t.Fatal("stop escaped its scope")
		}
	}
	id := "task:a:0"
	for range 2 {
		if _, err := r.stopRun(t.Context(), s, protocol.Envelope{Payload: protocol.Stop{RunID: &id, ConversationID: &r.conversation}}); err != nil {
			t.Fatalf("queued task could not be stopped: %v", err)
		}
	}
	if _, err := r.stopAllRuns(t.Context(), s, protocol.Envelope{Payload: protocol.MultiTaskControl{Action: "stop_all", ConversationID: &r.conversation}}); err != nil {
		t.Fatal(err)
	}
	if err := r.work.Recover(t.Context(), "restart"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "other", "foreign"} {
		c, err := r.work.Get(t.Context(), id)
		if err != nil || c.Scheduling.Closed != (id == "a") {
			t.Fatalf("wrong task stopped: %+v, %v", c, err)
		}
	}
}
