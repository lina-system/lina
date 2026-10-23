// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/lease"
)

type disconnectedResultProbe struct {
	recoveryCompletionProbe
	events chan worker.Event
}

func (p *disconnectedResultProbe) Events() <-chan worker.Event { return p.events }

func TestWorkerRecoveredSuccessAfterTransportLossIsDeliveredOnce(t *testing.T) {
	r := controlRuntime(t)
	ctx, now := t.Context(), time.Now()
	input, err := r.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: "recover-output", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Produce the result"})
	if err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "recovered-session", TurnID: "turn", TaskID: "task", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
	lost := worker.Event{Kind: worker.FinishEvent, Thread: thread, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: "connection_lost"}}
	done := worker.Event{Kind: worker.FinishEvent, Thread: thread, Finish: &worker.Finish{Status: worker.Done}}
	probe := &disconnectedResultProbe{
		recoveryCompletionProbe: recoveryCompletionProbe{workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Idle, Finish: done.Finish, EffectsKnown: true, Output: "Confirmed retained result"}}},
		events:                  make(chan worker.Event, 2),
	}
	probe.events <- lost
	probe.events <- lost
	close(probe.events)
	entry := &workerSession{origin: r, input: input, thread: thread, adapter: probe, workdir: t.TempDir(), packet: "Produce the result", output: "Partial output", active: map[string]lease.Lease{}, done: make(chan struct{}), close: func() error { return nil }, recovery: recovery.Record{ID: thread.ID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: input.ID, Thread: thread, State: "running"}}
	entry.observe()
	assertWorkerCompletionCounts(t, r, 1)
	if entry.recovery.State != "unknown" {
		t.Fatalf("transport loss settled execution: %s", entry.recovery.State)
	}
	entry.reconcileWorker(ctx)
	entry.reconcileWorker(ctx)
	// Repeating either observation, including after restoring the session,
	// must not admit another reaction or hide the confirmed output.
	if err := entry.finish(ctx, lost); err != nil {
		t.Fatal(err)
	}
	restored := &workerSession{origin: r, input: input, thread: thread, workdir: entry.workdir, packet: entry.packet}
	if err := restored.recoveredFinish(ctx, done, probe.snapshot.Output); err != nil {
		t.Fatal(err)
	}
	got, err := r.recordedWorker(ctx, thread.ID)
	if err != nil || got.Status != worker.Done || got.Output != probe.snapshot.Output || got.Live {
		t.Fatalf("transport-loss notice hid recovered success: %+v %v", got, err)
	}
	if entry.recovery.State != "finished" || probe.inputs != 0 {
		t.Fatalf("recovery state=%s worker inputs=%d", entry.recovery.State, probe.inputs)
	}
	assertWorkerCompletionCounts(t, r, 2)
}

func TestWorkerTransportLossCannotHideRecordedTerminal(t *testing.T) {
	r := controlRuntime(t)
	ctx, now := t.Context(), time.Now()
	input, err := r.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: "terminal-first", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Produce the result"})
	if err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "completed-session", TurnID: "turn", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
	entry := &workerSession{origin: r, input: input, thread: thread, output: "Confirmed result"}
	if err := entry.finish(ctx, worker.Event{Kind: worker.FinishEvent, Thread: thread, Finish: &worker.Finish{Status: worker.Done}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := entry.finish(ctx, worker.Event{Kind: worker.FinishEvent, Thread: thread, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: "connection_lost"}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.recordedWorker(ctx, thread.ID)
	if err != nil || got.Status != worker.Done || got.Output != entry.output {
		t.Fatalf("late transport loss hid terminal result: %+v %v", got, err)
	}
	assertWorkerCompletionCounts(t, r, 1)
}

func assertWorkerCompletionCounts(t *testing.T, r *conversationRuntime, want int) {
	t.Helper()
	err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, query := range []string{
			"SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion'",
			"SELECT count(*) FROM inbox WHERE source='worker-finish'",
		} {
			var got int
			if err := tx.QueryRowContext(t.Context(), query).Scan(&got); err != nil {
				return err
			}
			if got != want {
				t.Errorf("completion evidence/reaction count=%d, want=%d (%s)", got, want, query)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
