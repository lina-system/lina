// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

type transferProbe struct {
	workerProbe
	events   chan worker.Event
	next     string
	inputErr error
}

func (p *transferProbe) Events() <-chan worker.Event { return p.events }
func (p *transferProbe) Input(context.Context, worker.Thread, worker.Input) error {
	p.inputs++
	if p.inputErr != nil {
		return p.inputErr
	}
	p.snapshot.Thread.TurnID = p.next
	return nil
}

func TestSwitchTerminalConnectionFailureReleasesExecution(t *testing.T) {
	r, entry, probe := reviewWorkerFixture(t)
	released := false
	entry.active["old"] = lease.New(func() { released = true })
	entry.close = func() error {
		if !released {
			t.Error("confirmed terminal event retained hard activity until process close")
		}
		return nil
	}
	probe.events <- worker.Event{Thread: entry.thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: "connection"}}
	close(probe.events)
	entry.observe()
	if err := r.work.RequestRollback(t.Context(), "switch", r.person, "terminal provider failure"); err != nil {
		t.Fatal(err)
	}
	if err := r.work.CompleteRollback(t.Context(), "switch", r.person, "terminal event observed"); err != nil {
		t.Fatal("confirmed terminal failure retained execution", err)
	}
}

func TestSwitchRefusedInputKeepsCompletionBinding(t *testing.T) {
	r, entry, probe := reviewWorkerFixture(t)
	probe.snapshot.State = worker.Waiting
	probe.inputErr = worker.ErrInputRefused
	raw, _ := json.Marshal(workerArgs{Session: "thread", Turn: "old", Generation: 1, Binding: 1, Text: "continue"})
	in, err := workerSchema("worker_input", raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.workerOperation(t.Context(), "worker_input", "conversation-turn", ledger.Event{}, in)
	if err != nil || result.State != tools.Failed {
		t.Errorf("definitive refusal reported as ambiguous: %+v %v", result, err)
	}
	if entry.reservationTurn != "old" {
		t.Errorf("definitive refusal lost completion binding: %s", entry.reservationTurn)
	}
	probe.events <- worker.Event{Thread: entry.thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Interrupted}}
	close(probe.events)
	entry.observe()
	if err := r.work.RequestRollback(t.Context(), "switch", r.person, "interrupted after refusal"); err != nil {
		t.Fatal(err)
	}
	if err := r.work.CompleteRollback(t.Context(), "switch", r.person, "terminal event observed"); err != nil {
		t.Fatal("refused input retained execution after its completion", err)
	}
}

func TestSwitchInputOutcomeControlsReservation(t *testing.T) {
	for _, idle := range []bool{false, true} {
		t.Run(map[bool]string{false: "ambiguous-active-input", true: "refused-idle-input"}[idle], func(t *testing.T) {
			r, entry, probe := reviewWorkerFixture(t)
			r.activities = lifecycle.NewRegistry(nil)
			probe.inputErr = context.DeadlineExceeded
			if idle {
				probe.snapshot.State = worker.Idle
				probe.inputErr = worker.ErrInputRefused
			}
			raw, _ := json.Marshal(workerArgs{Session: "thread", Turn: "old", Generation: 1, Binding: 1, Text: "continue"})
			in, err := workerSchema("worker_input", raw)
			if err != nil {
				t.Fatal(err)
			}
			result, err := r.workerOperation(t.Context(), "worker_input", "conversation-turn", ledger.Event{}, in)
			if idle {
				if err != nil || result.State != tools.Failed || entry.reservation != "" {
					t.Fatal("refused idle input retained a new execution", result, err, entry.reservation)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) || result.State != tools.Unknown || !strings.HasPrefix(entry.reservationTurn, "unknown:") {
				t.Fatal("ambiguous input lost its unknown execution", result, err, entry.reservationTurn)
			}
			probe.events <- worker.Event{Thread: entry.thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Done}}
			close(probe.events)
			entry.observe()
			if err := r.work.RequestRollback(t.Context(), "switch", r.person, "reconcile input"); err != nil {
				t.Fatal(err)
			}
			err = r.work.CompleteRollback(t.Context(), "switch", r.person, "old turn completed")
			if idle && err != nil || !idle && !errors.Is(err, work.ErrActiveRun) {
				t.Fatal("rollback ignored input outcome", err)
			}
		})
	}
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

type refusedStartProbe struct {
	workerProbe
	err error
}

func (p *refusedStartProbe) Start(context.Context, lease.Lease, worker.StartSpec) (worker.Thread, error) {
	return worker.Thread{}, p.err
}

func TestSwitchStartRefusalReleasesOnlyAfterCleanup(t *testing.T) {
	for _, outcome := range []string{"refused", "ambiguous", "cleanup_failed"} {
		t.Run(outcome, func(t *testing.T) {
			r, entry, _ := reviewWorkerFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			startErr := errors.Join(worker.ErrInputRefused, context.Canceled)
			if outcome == "ambiguous" {
				startErr = context.Canceled
			}
			entry.adapter = &refusedStartProbe{err: startErr}
			closed, released := false, false
			entry.process = lease.New(func() {
				if !closed {
					t.Error("process lease released before cleanup")
				}
				released = true
			})
			entry.close = func() error {
				closed = true
				if err := r.work.CheckWorker(t.Context(), entry.reservation); err != nil {
					t.Errorf("reservation released before cleanup: %v", err)
				}
				if outcome == "cleanup_failed" {
					return errors.New("cleanup failed")
				}
				return nil
			}
			if _, err := entry.start(ctx, worker.StartSpec{}); !errors.Is(err, context.Canceled) {
				t.Fatal("lost launch error", err)
			}
			if !closed || !released {
				t.Fatal("failed start was not cleaned up")
			}
			if err := r.work.Recover(t.Context(), "restart"); err != nil {
				t.Fatal(err)
			}
			if err := r.work.RequestRollback(t.Context(), "switch", r.person, "launch failed"); err != nil {
				t.Fatal(err)
			}
			err := r.work.CompleteRollback(t.Context(), "switch", r.person, "check launch outcome")
			if outcome == "refused" {
				if err != nil || entry.reservation != "" {
					t.Fatalf("undispatched start retained reservation: %q %v", entry.reservation, err)
				}
			} else if !errors.Is(err, work.ErrActiveRun) {
				t.Fatal("uncertain start lost reservation", err)
			}
		})
	}
}
