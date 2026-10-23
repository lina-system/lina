// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker"
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
