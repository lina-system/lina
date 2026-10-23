// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
)

func TestForgeSettlementRetiresTheWorkerProcess(t *testing.T) {
	for _, state := range []string{"OPEN", "MERGED", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			service := newBootWork(t)
			closed := 0
			released := false
			process := lease.New(func() { released = true })
			thread := worker.Thread{ID: "thread", TurnID: "turn", TaskID: "a"}
			entry := &workerSession{close: func() error { closed++; return nil }, process: process, thread: thread}
			root := &conversationRuntime{work: service, workers: &workerRuntime{sessions: map[string]*workerSession{thread.ID: entry}}}
			d := &delegationRuntime{root: root, threads: map[string]worker.Thread{thread.TaskID: thread}, processByTask: map[string]lease.Lease{thread.TaskID: process}, leases: map[string]lease.Lease{}}
			d.reactor = reactor.NewScheduler(service, reactor.Hooks{
				Availability: func(context.Context) (work.Availability, error) {
					return work.Availability{Hosts: map[string]int{"local": 1}}, nil
				},
				Start: func(_ context.Context, c work.Card) (work.Run, error) {
					return work.Run{ID: "run:" + c.Task.ID, TaskID: c.Task.ID, Generation: 1, Worker: "codex", NodeID: "local", ThreadID: thread.ID, TurnID: thread.TurnID}, nil
				},
				ReadPR: func(context.Context, work.Card) (work.PullRequest, error) {
					pr := work.PullRequest{Number: 4, Head: "head", State: state, Mergeable: "MERGEABLE"}
					if state == "MERGED" {
						pr.MergeCommit = strings.Repeat("a", 40)
					}
					return pr, nil
				},
			})
			if err := d.reactor.Schedule(t.Context(), []work.Task{delegatedGraphTask("a", "a/**")}); err != nil {
				t.Fatal(err)
			}
			if err := d.reactor.Finished(t.Context(), "a", "run:a", "completed"); err != nil {
				t.Fatal(err)
			}
			if err := d.reactor.Observe(t.Context(), "a"); err != nil {
				t.Fatal(err)
			}
			d.retireSettled(t.Context(), "a")
			d.retireSettled(t.Context(), "a")
			wantClosed := state != "OPEN"
			if (closed != 0) != wantClosed || closed > 1 || released != wantClosed {
				t.Fatalf("%s closed=%d released=%v", state, closed, released)
			}
			root.workers.mu.Lock()
			_, live := root.workers.sessions[thread.ID]
			root.workers.mu.Unlock()
			d.mu.Lock()
			_, tracked := d.threads[thread.TaskID]
			_, leased := d.processByTask[thread.TaskID]
			d.mu.Unlock()
			if live == wantClosed || tracked == wantClosed || leased == wantClosed {
				t.Fatalf("%s live=%v tracked=%v leased=%v", state, live, tracked, leased)
			}
		})
	}
}
