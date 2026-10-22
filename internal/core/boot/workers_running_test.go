// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// Hold a real worker launch at an explicit fixture barrier. Opening an already
// active row must still finish; it must not wait for another worker's startup
// while occupying the canonical writer.
func TestConversationWorkerRunningOpenDuringStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell worker fixture")
	}
	r, _ := runningWorkerFixture(t)
	bin := t.TempDir()
	executable := filepath.Join(bin, "codex")
	script := "#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\n: > \"$0.started\"\nwhile [ ! -f \"$0.release\" ]; do sleep 0.01; done\nexit 1\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	raw, _ := json.Marshal(workerArgs{Workdir: t.TempDir(), TaskPacket: "hold at startup"})
	in, err := workerSchema("worker_start", raw)
	if err != nil {
		t.Fatal(err)
	}
	startedDone := make(chan struct{})
	var openedDone chan struct{}
	defer func() {
		if err := os.WriteFile(executable+".release", nil, 0600); err != nil {
			t.Error(err)
		}
		<-startedDone
		if openedDone != nil {
			<-openedDone
		}
	}()
	go func() {
		defer close(startedDone)
		if _, err := r.workerOperation(t.Context(), "worker_start", "other-turn", ledger.Event{}, in); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(executable + ".started"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("worker did not reach startup barrier")
		case <-ticker.C:
		}
	}
	openedDone = make(chan struct{})
	opened := make(chan error, 1)
	go func() {
		defer close(openedDone)
		opened <- r.background.list.Open(ctx, r.person, runs.ListRef{Source: "session", ID: "fixture"})
	}()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("opening a running row blocked the canonical writer behind worker startup")
	}
}
