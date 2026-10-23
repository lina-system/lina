// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
)

func TestWorkerCompletionAfterConnectionLoss(t *testing.T) {
	for _, status := range []worker.FinishStatus{worker.Done, worker.Failed} {
		t.Run(string(status), func(t *testing.T) {
			r := controlRuntime(t)
			now := time.Now()
			input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "recover-result", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Produce a result"})
			if err != nil {
				t.Fatal(err)
			}
			thread := worker.Thread{ID: "thread", TurnID: "turn", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
			entry := &workerSession{origin: r, input: input, thread: thread, workdir: t.TempDir(), packet: "Produce a result"}
			lost := worker.Event{Kind: worker.FinishEvent, Thread: thread, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: "connection_lost"}}
			for range 2 {
				if err := entry.finish(t.Context(), lost); err != nil {
					t.Fatal(err)
				}
			}
			// A replacement process reads the same attempt and exact turn.
			thread.ProcessGeneration++
			thread.BindingEpoch++
			entry.thread = thread
			finish := &worker.Finish{Status: status}
			if status == worker.Failed {
				finish.Failure = worker.WorkerFailure
			}
			confirmed := worker.Event{Kind: worker.FinishEvent, Thread: thread, Finish: finish}
			for range 2 {
				if err := entry.recoveredFinish(t.Context(), confirmed, "Confirmed output"); err != nil {
					t.Fatal(err)
				}
			}
			got, err := r.recordedWorker(t.Context(), thread.ID)
			if err != nil || got.Status != status || got.Failure != finish.Failure || got.Output != "Confirmed output" {
				t.Fatalf("confirmed result lost: %+v, err=%v", got, err)
			}
			// A delayed transport notice cannot replace the confirmed result.
			if err := entry.finish(t.Context(), lost); err != nil {
				t.Fatal(err)
			}
			var completions, losses, queued int
			err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion'").Scan(&completions); err != nil {
					return err
				}
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE source='worker-finish' AND json_extract(body,'$.failure')='connection'").Scan(&losses); err != nil {
					return err
				}
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE source='worker-finish'").Scan(&queued)
			})
			wantQueued := 1
			if status == worker.Done {
				wantQueued++
			}
			if err != nil || completions != 2 || losses != 1 || queued != wantQueued {
				t.Fatalf("completion admission: records=%d losses=%d queued=%d want=%d err=%v", completions, losses, queued, wantQueued, err)
			}
		})
	}
}

func TestWorkerOutputSurvivesDelayedCompletion(t *testing.T) {
	for _, order := range []string{"ordinary", "delayed_finish", "delayed_message_and_finish"} {
		t.Run(order, func(t *testing.T) {
			r := controlRuntime(t)
			now := time.Now()
			input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "delayed-result", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Produce a result"})
			if err != nil {
				t.Fatal(err)
			}
			previous := worker.Thread{ID: "thread", TurnID: "previous", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
			current := previous
			current.TurnID = "current"
			probe := &failedRecoveryProbe{events: make(chan worker.Event, 5)}
			entry := &workerSession{origin: r, input: input, thread: current, adapter: probe, workdir: t.TempDir(), active: map[string]lease.Lease{}, done: make(chan struct{}), close: func() error { return nil }}
			late := worker.Event{Kind: worker.FinishEvent, Thread: previous, Finish: &worker.Finish{Status: worker.Done}}
			if err := entry.recoveredFinish(t.Context(), late, "Previous result"); err != nil {
				t.Fatal(err)
			}
			probe.events <- worker.Event{Kind: worker.ProgressEvent, Thread: current, Progress: &worker.Progress{Phase: "item_completed", Text: "Current result"}}
			if order == "delayed_message_and_finish" {
				probe.events <- worker.Event{Kind: worker.ProgressEvent, Thread: previous, Progress: &worker.Progress{Phase: "item_completed", Text: "Previous result"}}
			}
			if order != "ordinary" {
				probe.events <- late
				probe.events <- late
			}
			probe.events <- worker.Event{Kind: worker.FinishEvent, Thread: current, Finish: &worker.Finish{Status: worker.Done}}
			close(probe.events)
			entry.observe()
			got, err := r.recordedWorker(t.Context(), current.ID)
			if err != nil || got.Turn != current.TurnID || got.Output != "Current result" || got.Status != worker.Done {
				t.Errorf("current completion lost its output: %+v err=%v", got, err)
			}
			var completions, priorResults, queued int
			err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion'").Scan(&completions); err != nil {
					return err
				}
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion' AND json_extract(body,'$.turn')='previous' AND json_extract(body,'$.output')='Previous result'").Scan(&priorResults); err != nil {
					return err
				}
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE source='worker-finish'").Scan(&queued)
			})
			if err != nil || completions != 2 || priorResults != 1 || queued != 2 {
				t.Fatalf("result admission: completions=%d previous=%d queued=%d err=%v", completions, priorResults, queued, err)
			}
		})
	}
}
