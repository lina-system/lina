// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

type transferProbe struct {
	workerProbe
	events chan worker.Event
	next   string
}

func (p *transferProbe) Events() <-chan worker.Event { return p.events }
func (p *transferProbe) Input(context.Context, worker.Thread, worker.Input) error {
	p.inputs++
	p.snapshot.Thread.TurnID = p.next
	return nil
}
func reviewWorkerFixture(t *testing.T) (*conversationRuntime, *workerSession, *transferProbe) {
	r, _, agent, _ := managementFixture(t)
	r.ctx = t.Context()
	r.work = work.New(r.db)
	dir := t.TempDir()
	task := work.Task{ID: "external", PersonID: r.person, AgentID: agent, ConversationID: "conversation", Title: "Work", Done: "Result", ExternalControl: true, ArtifactRoots: []string{dir}}
	if err := r.work.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if err := r.work.ApproveSwitch(t.Context(), work.Switch{ID: "switch", Owner: r.person, Approval: "approval", Snapshot: strings.Repeat("a", 64), Tasks: []string{task.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := r.work.ActivateSwitch(t.Context(), "switch", r.person, "proof"); err != nil {
		t.Fatal(err)
	}
	if err := r.work.ReserveWorker(t.Context(), "worker", dir); err != nil {
		t.Fatal(err)
	}
	probe := &transferProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "old", ProcessGeneration: 1, BindingEpoch: 1}, State: worker.Active}}, events: make(chan worker.Event, 2), next: "new"}
	entry := &workerSession{adapter: probe, origin: r, thread: probe.snapshot.Thread, workdir: dir, reservation: "worker", reservationTurn: "old", active: map[string]lease.Lease{"old": lease.New(func() {})}, done: make(chan struct{}), close: func() error { return nil }}
	r.workers = &workerRuntime{sessions: map[string]*workerSession{"thread": entry}}
	return r, entry, probe
}
func TestSwitchConnectionLossDoesNotReleaseExecution(t *testing.T) {
	r, entry, probe := reviewWorkerFixture(t)
	released := false
	entry.active["old"] = lease.New(func() { released = true })
	entry.close = func() error {
		if released {
			t.Error("connection loss released activity before process cleanup")
		}
		return nil
	}
	probe.events <- worker.Event{Thread: entry.thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: "connection_lost"}}
	close(probe.events)
	entry.observe()
	if err := r.work.RequestRollback(t.Context(), "switch", r.person, "connection lost"); err != nil {
		t.Fatal(err)
	}
	if err := r.work.CompleteRollback(t.Context(), "switch", r.person, "attempt release"); !errors.Is(err, work.ErrActiveRun) {
		t.Fatal("unobserved execution released", err)
	}
}
func TestSwitchInputRebindsBeforeOldCompletion(t *testing.T) {
	r, entry, probe := reviewWorkerFixture(t)
	old := entry.thread
	raw, _ := json.Marshal(workerArgs{Session: "thread", Turn: "old", Generation: 1, Binding: 1, Text: "continue"})
	in, err := workerSchema("worker_input", raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.workerOperation(t.Context(), "worker_input", "conversation-turn", ledger.Event{}, in)
	if err != nil || result.State != tools.Applied {
		t.Fatal(result, err)
	}
	probe.events <- worker.Event{Thread: old, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Done}}
	close(probe.events)
	entry.observe()
	if err = r.work.RequestRollback(t.Context(), "switch", r.person, "rebind test"); err != nil {
		t.Fatal(err)
	}
	if err = r.work.CompleteRollback(t.Context(), "switch", r.person, "attempt release"); !errors.Is(err, work.ErrActiveRun) {
		t.Fatal("old completion released a new turn", err)
	}
	if entry.reservationTurn != "new" {
		t.Fatal("reservation did not follow new turn", entry.reservationTurn)
	}
}
