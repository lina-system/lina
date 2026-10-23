// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
)

type runningWorkerProbe struct {
	worker.Adapter
	snapshot       worker.ThreadSnapshot
	err            error
	beforeSnapshot func()
}

func (p *runningWorkerProbe) Snapshot(context.Context, worker.Thread) (worker.ThreadSnapshot, error) {
	if p.beforeSnapshot != nil {
		p.beforeSnapshot()
	}
	return p.snapshot, p.err
}

type runningTransactionProbe struct {
	runs.ListCanon
	inside atomic.Bool
}

func (p *runningTransactionProbe) Read(ctx context.Context, f func(*canon.Tx) error) error {
	p.inside.Store(true)
	defer p.inside.Store(false)
	return p.ListCanon.Read(ctx, f)
}

func (p *runningTransactionProbe) Write(ctx context.Context, f func(*canon.Tx) error) error {
	p.inside.Store(true)
	defer p.inside.Store(false)
	return p.ListCanon.Write(ctx, f)
}

func TestConversationWorkerRunningObservesOutsideCanon(t *testing.T) {
	r, probe := runningWorkerFixture(t)
	db := &runningTransactionProbe{ListCanon: r.db}
	r.background.list = runs.NewRunningList(db, map[string]runs.ListSource{"session": r.background.sessions})
	var observed atomic.Int32
	probe.beforeSnapshot = func() {
		observed.Add(1)
		if db.inside.Load() {
			t.Error("worker RPC executed inside a canonical transaction")
		}
	}
	if err := r.publishRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.background.list.Open(t.Context(), r.person, runs.ListRef{Source: "session", ID: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if observed.Load() == 0 {
		t.Fatal("running publication never observed the worker")
	}
}

func runningWorkerFixture(t *testing.T) (*conversationRuntime, *runningWorkerProbe) {
	t.Helper()
	r := runRuntime(t)
	if r.workers.recoveryStop != nil {
		r.workers.recoveryStop()
		<-r.workers.recoveryDone
		r.workers.recoveryStop = nil
	}
	// Stop the periodic owner-view observer before installing mutable unit
	// probes. Installed-Core scenarios exercise both live view producers.
	if r.ownerViews != nil {
		r.ownerViews.stop()
		<-r.ownerViews.done
	}
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
	r.background.sessions = &workerListSource{workers: r.workers, person: r.person}
	r.background.list = runs.NewRunningList(r.db, map[string]runs.ListSource{"session": r.background.sessions})
	if err := r.background.sessions.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r, probe
}

func TestConversationWorkerRunningStates(t *testing.T) {
	r, probe := runningWorkerFixture(t)
	agent, err := r.agents.Get(t.Context(), r.agent)
	if err != nil {
		t.Fatal(err)
	}
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
			if err := r.background.sessions.refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
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
			if row.Ref != (runs.ListRef{Source: "session", ID: "fixture"}) || row.View.State != tc.want || row.View.AgentName != agent.Name {
				t.Fatalf("worker projection: %+v", row)
			}
			owner, tool := strings.Index(row.View.Summary, agent.Name), strings.Index(row.View.Summary, "Codex")
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
	if err = r.background.sessions.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = r.background.list.Dismiss(t.Context(), r.person, ref); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.background.list.Build(t.Context(), r.person)
	if err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("dismissed failed worker retained: %+v %v", snapshot, err)
	}
	r.workers.closed = true
	if err = r.background.sessions.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = r.background.list.Build(t.Context(), r.person)
	if err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("closed runtime retained: %+v %v", snapshot, err)
	}
}

// The canonical writer must not wait for the live registry while a worker is
// being registered. Hold that registry explicitly; the installed-Core scenario
// separately exercises the real worker_start process boundary.
func TestConversationWorkerRunningOpenDuringRegistryUpdate(t *testing.T) {
	r, _ := runningWorkerFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	opened := make(chan error, 1)
	r.workers.mu.Lock()
	go func() {
		opened <- r.background.list.Open(ctx, r.person, runs.ListRef{Source: "session", ID: "fixture"})
	}()
	var err error
	timedOut := false
	select {
	case err = <-opened:
	case <-ctx.Done():
		timedOut, err = true, ctx.Err()
	}
	r.workers.mu.Unlock()
	if timedOut {
		<-opened // Join the callback after releasing the intentionally held lock.
	}
	if err != nil {
		t.Fatalf("opening a running row waited for the worker registry: %v", err)
	}
}

func TestConversationWorkerProgressVisibilityMatchesRunning(t *testing.T) {
	for _, action := range []string{"open", "dismiss"} {
		t.Run(action, func(t *testing.T) {
			r, probe := runningWorkerFixture(t)
			check := func(want int) {
				t.Helper()
				p, err := r.ownerSnapshot(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if err = r.background.sessions.refresh(t.Context()); err != nil {
					t.Fatal(err)
				}
				running, err := r.background.list.Build(t.Context(), r.person)
				if err != nil || p.Rows == nil || len(*p.Rows) != want || len(running.Entries) != want {
					t.Fatalf("views disagree: progress=%+v running=%+v want=%d err=%v", p.Rows, running, want, err)
				}
				if want > 0 {
					line := (*p.Rows)[0].Line
					if line.State == nil || string(*line.State) != running.Entries[0].View.State {
						t.Fatalf("execution states disagree: progress=%+v running=%+v", line, running.Entries[0])
					}
					if line.Stage != "unknown" || line.Percent != nil || line.JustFinished != "unknown" || line.Next != "unknown" {
						t.Fatalf("worker state invented a plan outcome: %+v", line)
					}
				}
			}
			check(1)
			probe.snapshot.State = worker.Idle
			check(0)
			probe.snapshot.State = worker.Active
			check(1)
			probe.err = errors.New("snapshot unavailable")
			check(1)
			probe.err = nil
			probe.snapshot.State = worker.Dead
			check(1)
			ref := runs.ListRef{Source: "session", ID: "fixture"}
			var err error
			if action == "open" {
				err = r.background.list.Open(t.Context(), r.person, ref)
			} else {
				err = r.background.list.Dismiss(t.Context(), r.person, ref)
			}
			if err != nil {
				t.Fatal(err)
			}
			check(0)
		})
	}
}
