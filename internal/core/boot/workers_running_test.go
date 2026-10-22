// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
)

type runningWorkerProbe struct {
	worker.Adapter
	snapshot worker.ThreadSnapshot
	err      error
}

func (p *runningWorkerProbe) Snapshot(context.Context, worker.Thread) (worker.ThreadSnapshot, error) {
	return p.snapshot, p.err
}

func runningWorkerFixture(t *testing.T) (*conversationRuntime, *runningWorkerProbe) {
	t.Helper()
	r := runRuntime(t)
	probe := &runningWorkerProbe{snapshot: worker.ThreadSnapshot{State: worker.Active, Worker: "codex"}}
	body, err := secrets.Mask("Compare the options\nwithout changing files")
	if err != nil {
		t.Fatal(err)
	}
	entry := &workerSession{adapter: probe, origin: r, input: ledger.Event{Body: body}, thread: worker.Thread{ID: "fixture"}, started: time.Unix(100, 0), packet: "private worker instructions", done: make(chan struct{})}
	r.workers = &workerRuntime{sessions: map[string]*workerSession{
		"fixture": entry,
		// An out-of-scope session must be filtered before touching its adapter.
		"foreign": {origin: &conversationRuntime{person: "someone-else"}},
	}}
	t.Cleanup(func() { r.workers = nil })
	r.background.list = runs.NewRunningList(r.db, map[string]runs.ListSource{"session": workerListSource{workers: r.workers}})
	return r, probe
}

func TestConversationWorkerRunningStates(t *testing.T) {
	r, probe := runningWorkerFixture(t)
	for _, tc := range []struct {
		name    string
		state   worker.ThreadState
		pending []worker.Request
		err     error
		want    string
	}{
		{name: "active", state: worker.Active, want: "in_progress"},
		{name: "approval", state: worker.Waiting, pending: []worker.Request{{Method: "item/commandExecution/requestApproval"}}, want: "needs_input"},
		{name: "question", state: worker.Waiting, pending: []worker.Request{{Method: "item/tool/requestUserInput"}}, want: "needs_input"},
		{name: "unexplained wait", state: worker.Waiting, want: "unknown"},
		{name: "unknown", state: worker.Unknown, want: "unknown"},
		{name: "snapshot unavailable", err: errors.New("worker unavailable"), want: "unknown"},
		{name: "dead", state: worker.Dead, want: "failed"},
		{name: "idle", state: worker.Idle},
		{name: "continued", state: worker.Active, want: "in_progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe.snapshot.State, probe.snapshot.Pending, probe.err = tc.state, tc.pending, tc.err
			snapshot, err := r.background.list.Build(t.Context(), r.person)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(snapshot.Entries) != 0 {
					t.Fatalf("idle worker retained: %+v", snapshot)
				}
				return
			}
			if len(snapshot.Entries) != 1 {
				t.Fatalf("running entries: %+v", snapshot)
			}
			row := snapshot.Entries[0]
			if row.Ref != (runs.ListRef{Source: "session", ID: "fixture"}) || row.View.State != tc.want || row.View.AgentName != "LINA" {
				t.Fatalf("worker projection: %+v", row)
			}
			owner, tool := strings.Index(row.View.Summary, "LINA"), strings.Index(row.View.Summary, "Codex")
			if owner < 0 || tool <= owner || !strings.Contains(row.View.Summary, "Compare the options") || strings.ContainsAny(row.View.Summary, "\n\r") || strings.Contains(row.View.Summary, "private worker instructions") {
				t.Fatalf("unsafe or unattributed summary: %q", row.View.Summary)
			}
		})
	}
}

func TestConversationWorkerRunningPublicationAndActions(t *testing.T) {
	r, probe := runningWorkerFixture(t)
	ref := runs.ListRef{Source: "session", ID: "fixture"}
	if err := r.background.list.Open(t.Context(), r.person, ref); err != nil {
		t.Fatal(err)
	}
	if err := r.background.list.Dismiss(t.Context(), r.person, ref); !errors.Is(err, runs.ErrListItem) {
		t.Fatalf("dismiss hid active worker: %v", err)
	}
	summary, err := r.workerRunningSummary(t.Context(), r.person, ref.ID, 0)
	if err != nil || !strings.Contains(summary, "Codex") {
		t.Fatalf("worker summary: %q %v", summary, err)
	}
	if _, err = r.workerRunningSummary(t.Context(), "someone-else", ref.ID, 0); err == nil {
		t.Fatal("foreign person read summary")
	}
	if _, err = r.workerRunningSummary(t.Context(), r.person, ref.ID, 1); err == nil {
		t.Fatal("unpaged summary accepted an offset")
	}
	if err = r.publishRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	initial, live, err := r.topics.Subscribe("running", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if initial.Snapshot == nil || len(initial.Snapshot.Items) != 1 || initial.Snapshot.Items[0].Key != "session:fixture" {
		t.Fatalf("session namespace missing: %+v", initial)
	}
	probe.snapshot.State = worker.Idle
	if err = r.publishRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	frame, err := live.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var removed protocol.RunningUpdate
	if err = json.Unmarshal(frame.Event.Data, &removed); err != nil || removed.RunID == nil || *removed.RunID != "session:fixture" || removed.Removed == nil || !*removed.Removed {
		t.Fatalf("missing live removal: %+v %v", removed, err)
	}
	retained, reconnect, err := r.topics.Subscribe("running", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnect.Close()
	if retained.Snapshot == nil || len(retained.Snapshot.Items) != 0 {
		t.Fatalf("idle worker retained on reconnect: %+v", retained)
	}
	probe.snapshot.State = worker.Dead
	if err = r.background.list.Dismiss(t.Context(), r.person, ref); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.background.list.Build(t.Context(), r.person)
	if err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("dismissed failed worker retained: %+v %v", snapshot, err)
	}
	r.workers.closed = true
	snapshot, err = r.background.list.Build(t.Context(), r.person)
	if err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("closed runtime retained: %+v %v", snapshot, err)
	}
}
