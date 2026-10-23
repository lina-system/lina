// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
)

type deadlineLaneAdapter struct {
	worker.Adapter
	deadlines []time.Time
	errors    []error
}

func (a *deadlineLaneAdapter) Snapshot(ctx context.Context, _ worker.Thread) (worker.ThreadSnapshot, error) {
	deadline, _ := ctx.Deadline()
	a.deadlines = append(a.deadlines, deadline)
	<-ctx.Done()
	a.errors = append(a.errors, ctx.Err())
	return worker.ThreadSnapshot{}, ctx.Err()
}

func TestSessionLanesSnapshotDeadline(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "page deadline"
		if canceled {
			name = "caller canceled"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := &conversationRuntime{person: "person", agent: "agent", main: "main"}
				r.workers = &workerRuntime{sessions: map[string]*workerSession{}}
				adapter := &deadlineLaneAdapter{}
				for _, id := range []string{"a", "b"} {
					thread := worker.Thread{ID: id, TaskID: "task-" + id, TurnID: "turn-" + id, ProcessGeneration: 7}
					r.workers.sessions[id] = &workerSession{adapter: adapter, origin: r, thread: thread}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				wantDuration, wantErr := 5*time.Second, context.DeadlineExceeded
				if canceled {
					cancel()
					wantDuration, wantErr = 0, context.Canceled
				}
				start := time.Now()
				rows, next := r.sessionLanes(ctx, r.person, "", map[string]string{r.agent: "LINA"}, 32)
				if elapsed := time.Since(start); elapsed != wantDuration {
					t.Fatalf("page used %s of virtual time, want %s", elapsed, wantDuration)
				}
				if len(rows) != 2 || next != nil || len(adapter.deadlines) != 2 {
					t.Fatalf("incomplete expired page: %+v, cursor %v", rows, next)
				}
				for i, row := range rows {
					if row.State != "unknown" || row.TaskID != "task-"+row.ThreadID || row.TurnID != "turn-"+row.ThreadID || row.Generation != "7" || row.AgentID != r.agent {
						t.Fatalf("expired snapshot lost identity or state: %+v", row)
					}
					if adapter.deadlines[i] != start.Add(5*time.Second) || adapter.errors[i] != wantErr {
						t.Fatalf("snapshot did not share page cancellation: %v, %v", adapter.deadlines, adapter.errors)
					}
				}
			})
		})
	}
}
