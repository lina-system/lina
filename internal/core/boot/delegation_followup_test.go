// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/tools"
)

type scheduledFollowProbe struct {
	workerProbe
	ended  bool
	starts int
}

func (p *scheduledFollowProbe) Input(ctx context.Context, thread worker.Thread, input worker.Input) error {
	if p.ended {
		p.starts++
	}
	return p.workerProbe.Input(ctx, thread, input)
}

func (p *scheduledFollowProbe) Steer(ctx context.Context, thread worker.Thread, input worker.Input) error {
	if p.ended {
		return errors.Join(worker.ErrInputRefused, errors.New("turn already ended"))
	}
	return p.workerProbe.Input(ctx, thread, input)
}

func TestScheduledFollowUp(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	thread := worker.Thread{ID: "session", TaskID: "scheduled", TurnID: "turn", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
	probe := &scheduledFollowProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}}
	entry := &workerSession{scheduled: true, adapter: probe, origin: r, thread: thread}
	r.workers.sessions[thread.ID] = entry
	t.Cleanup(func() { delete(r.workers.sessions, thread.ID) })
	scheduler := reactor.NewScheduler(r.work, reactor.Hooks{
		Availability: func(context.Context) (work.Availability, error) {
			return work.Availability{Hosts: map[string]int{"local": 1}}, nil
		},
		Start: func(context.Context, work.Card) (work.Run, error) {
			return work.Run{ID: "attempt", TaskID: thread.TaskID, Generation: 1, ThreadID: thread.ID, TurnID: thread.TurnID, NodeID: "local", Worker: "codex"}, nil
		},
	})
	if err := scheduler.Schedule(t.Context(), []work.Task{stopTask(r, thread.TaskID)}); err != nil {
		t.Fatal(err)
	}
	request := protocol.Envelope{MessageID: "followup", Payload: protocol.FollowUp{RunID: "task:scheduled:1", ConversationID: &r.conversation, Text: "Include the failure details."}}
	for range 2 {
		if _, err := r.followRun(t.Context(), clientsrv.Session{PersonID: r.person}, request); err != nil {
			t.Fatalf("owner could not steer the active scheduled turn: %v", err)
		}
	}
	if probe.inputs != 1 {
		t.Fatalf("follow-up sent %d times", probe.inputs)
	}
	request.MessageID = "other-followup"
	if _, err := r.followRun(t.Context(), clientsrv.Session{PersonID: "foreign"}, request); err == nil {
		t.Fatal("foreign owner steered worker")
	}
	other := "other-conversation"
	foreign := request
	foreign.Payload = protocol.FollowUp{RunID: "task:scheduled:1", ConversationID: &other, Text: "foreign"}
	if _, err := r.followRun(t.Context(), clientsrv.Session{PersonID: r.person}, foreign); err == nil {
		t.Fatal("foreign conversation steered worker")
	}
	args, _ := json.Marshal(workerArgs{Session: thread.ID, Turn: thread.TurnID, Generation: 1, Binding: 1, Text: "model input"})
	in, err := workerSchema("worker_input", args)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := r.workerOperation(t.Context(), "worker_input", "model-turn", ledger.Event{}, in); err != nil || result.State != tools.Failed {
		t.Fatalf("model bypassed scheduled ownership: %+v %v", result, err)
	}
	for _, state := range []worker.ThreadState{worker.Idle, worker.Unknown, worker.Waiting, worker.Dead} {
		probe.snapshot.State = state
		if _, err := r.followRun(t.Context(), clientsrv.Session{PersonID: r.person}, request); err == nil {
			t.Fatalf("owner started scheduled turn from %s", state)
		}
	}
	if probe.inputs != 1 {
		t.Fatalf("refused control reached adapter: %d", probe.inputs)
	}
	// The worker can finish after Core reads the active snapshot. The adapter
	// must receive a steer-only operation, not its idle-or-active Input API.
	probe.snapshot.State, probe.ended = worker.Active, true
	if _, err := r.followRun(t.Context(), clientsrv.Session{PersonID: r.person}, request); err == nil || probe.starts != 0 {
		t.Fatalf("owner follow-up started a new scheduled turn: starts=%d err=%v", probe.starts, err)
	}
}
