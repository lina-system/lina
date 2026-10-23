// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestScheduledWorkerKeepsItsDurableRunningSource(t *testing.T) {
	r, probe := runningWorkerFixture(t)
	r.workers.sessions["scheduled"] = &workerSession{
		scheduled: true, adapter: probe, origin: r,
		thread: worker.Thread{ID: "scheduled", TaskID: "durable-task"},
		done:   make(chan struct{}),
	}
	if err := r.background.sessions.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.background.list.Build(t.Context(), r.person)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Ref != (runs.ListRef{Source: "session", ID: "fixture"}) {
		t.Fatalf("scheduled task also projected as a conversation session: %+v", snapshot.Entries)
	}
}
