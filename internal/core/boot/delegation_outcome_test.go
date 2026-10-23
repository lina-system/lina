// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/kit/lease"
)

func TestScheduledOutcome(t *testing.T) {
	for _, finish := range []worker.Finish{
		{Status: worker.Done},
		{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "worker_failed"},
		{Status: worker.Interrupted},
		{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: "connection_lost"},
	} {
		t.Run(string(finish.Status)+"/"+finish.Code, func(t *testing.T) {
			r := controlRuntime(t)
			now := time.Now()
			input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "scheduled-owner", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Inspect the assigned files."})
			if err != nil {
				t.Fatal(err)
			}
			thread := worker.Thread{ID: "scheduled-session", TurnID: "turn", TaskID: "task", AttemptID: "task:1", ProcessGeneration: 1, BindingEpoch: 1}
			entry := &workerSession{adapter: &mailboxProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}, tier: "pull"}, scheduled: true, origin: r, input: input, thread: thread, packet: "Inspect the assigned files.", workdir: t.TempDir()}
			r.workers.mu.Lock()
			r.workers.sessions[thread.ID] = entry
			r.workers.mu.Unlock()
			t.Cleanup(func() { r.workers.mu.Lock(); delete(r.workers.sessions, thread.ID); r.workers.mu.Unlock() })
			d := &delegationRuntime{root: r, adapter: &codex.Session{}, done: make(chan struct{}), workerEvents: make(chan worker.Event), leases: map[string]lease.Lease{}, threads: map[string]worker.Thread{thread.TaskID: thread}}
			d.reactor = reactor.NewScheduler(r.work, reactor.Hooks{
				Availability: func(context.Context) (work.Availability, error) {
					return work.Availability{Hosts: map[string]int{"local": 1}}, nil
				},
				Start: func(context.Context, work.Card) (work.Run, error) {
					return work.Run{ID: "task:1", TaskID: thread.TaskID, Generation: 1, ThreadID: thread.ID, TurnID: thread.TurnID, Worker: "codex", NodeID: "local"}, nil
				},
				Return: func(context.Context, work.Card) (work.Run, error) {
					next := thread
					next.TurnID = "next-turn"
					d.mu.Lock()
					d.threads[thread.TaskID] = next
					d.mu.Unlock()
					return work.Run{ID: "task:2", TaskID: thread.TaskID, Generation: 2, ThreadID: thread.ID, TurnID: next.TurnID, Worker: "codex", NodeID: "local"}, nil
				},
			})
			if err := d.reactor.Schedule(t.Context(), []work.Task{stopTask(r, thread.TaskID)}); err != nil {
				t.Fatal(err)
			}
			if _, err := entry.storeSteering(t.Context(), "pending-at-stop", "pending correction"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			go d.events(ctx)
			t.Cleanup(func() { cancel(); <-d.done })
			send := func(event worker.Event) {
				t.Helper()
				select {
				case d.workerEvents <- event:
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				}
			}
			const output = "Worker report: inspected the files; the build could not run."
			send(worker.Event{Thread: thread, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "item_completed", ItemID: "answer", Text: output}})
			for _, change := range []func(*worker.Thread){
				func(v *worker.Thread) { v.TurnID = "old-turn" },
				func(v *worker.Thread) { v.TaskID = "other-task" },
				func(v *worker.Thread) { v.AttemptID = "other-attempt" },
				func(v *worker.Thread) { v.ProcessGeneration++ },
				func(v *worker.Thread) { v.BindingEpoch++ },
			} {
				stale := thread
				change(&stale)
				send(worker.Event{Thread: stale, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "item_completed", Text: "foreign output"}})
			}
			// A duplicate completion must neither replace the report nor enqueue a
			// second persona follow-up. The unbuffered send is an event barrier.
			for range 2 {
				send(worker.Event{Thread: thread, Kind: worker.FinishEvent, Finish: &finish})
			}
			send(worker.Event{Thread: thread, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "barrier"}})
			messages, mailErr := r.work.SteeringMessages(t.Context(), "fake:"+thread.ID)
			wantMail := "stored"
			if finish.Status == worker.Done || finish.Status == worker.Interrupted {
				wantMail = "refused"
			}
			if mailErr != nil || len(messages) != 1 || messages[0].State != wantMail {
				t.Fatalf("scheduled mailbox after finish: %+v %v", messages, mailErr)
			}
			got, err := r.recordedWorker(t.Context(), thread.ID)
			if err != nil || got.Output != output || got.Status != finish.Status || got.Failure != finish.Failure || got.Live {
				t.Fatalf("scheduled result lost: output=%q status=%q failure=%q live=%v err=%v", got.Output, got.Status, got.Failure, got.Live, err)
			}
			var count int
			if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE source='worker-finish' AND conversation=?", r.conversation).Scan(&count)
			}); err != nil || count != 1 {
				t.Fatalf("follow-up count=%d: %v", count, err)
			}
			foreign := newConversationView(r, "other-agent", "other-conversation")
			if _, err := foreign.recordedWorker(t.Context(), thread.ID); err == nil {
				t.Fatal("result escaped its originating conversation")
			}
			card, err := r.work.Get(t.Context(), thread.TaskID)
			if err != nil || card.State == "done" || card.Verdict != "" || card.Receipt != nil || card.Scheduling.Landed {
				t.Fatalf("worker text became delivery: %+v %v", card, err)
			}
			if card.Active != (finish.Code == "connection_lost") {
				t.Fatalf("terminal observation changed reservation incorrectly: %+v", card)
			}
			events, err := r.events(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				if event.ScaffoldKind == "worker_completion" {
					message, err := event.Message()
					if err != nil {
						t.Fatal(err)
					}
					evidence, untrusted := message.Evidence()
					if !untrusted || !evidence.Untrusted {
						t.Fatal("worker output became trusted model context")
					}
					found = true
				}
			}
			if !found {
				t.Fatal("worker output absent from model history")
			}
			if finish.Status == worker.Done {
				// A late item can arrive while the completed turn is still bound.
				// It must not become the next conflict-return turn's report.
				send(worker.Event{Thread: thread, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "item_completed", Text: "late prior report"}})
				send(worker.Event{Thread: thread, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "barrier"}})
				if _, err := r.work.ObservePullRequest(t.Context(), thread.TaskID, "task:1", work.PullRequest{Number: 1, Head: "head", State: "OPEN", Mergeable: "CONFLICTING"}); err != nil {
					t.Fatal(err)
				}
				if err := d.reactor.Wake(t.Context()); err != nil {
					t.Fatal(err)
				}
				next := thread
				next.TurnID = "next-turn"
				send(worker.Event{Thread: next, Kind: worker.FinishEvent, Finish: &finish})
				send(worker.Event{Thread: next, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "barrier"}})
				got, err := r.recordedWorker(t.Context(), thread.ID)
				if err != nil || got.Turn != next.TurnID || got.Output != "" {
					t.Fatalf("prior report leaked into next turn: %+v %v", got, err)
				}
			}
		})
	}
}
