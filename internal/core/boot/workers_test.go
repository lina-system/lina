// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestWorkerSchemaRejectsInjectedSettingsAndIncompleteControls(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"worker_start", `{"workdir":"/work","task_packet":"task","sandbox":"disabled"}`},
		{"worker_start", `{"workdir":"relative","task_packet":"task"}`},
		{"worker_start", `{"workdir":"/work","task_packet":"task","agent":"foreign"}`},
		{"worker_input", `{"session":"thread","text":"do work"}`},
		{"worker_interrupt", `{"session":"thread","turn":"turn","generation":1,"binding":1,"reason":"stall"}`},
		{"worker_snapshot", `{"session":"thread","task_packet":"replacement"}`},
	} {
		if _, err := workerSchema(tc.name, json.RawMessage(tc.raw)); err == nil {
			t.Fatalf("accepted %s %s", tc.name, tc.raw)
		}
	}
}

func TestWorkerUnknownMethodRefusedBeforeLaunch(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	var err error
	r.skills, err = skills.Open(t.TempDir(), r.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.skills.Close()
	in, err := workerSchema("worker_start", json.RawMessage(`{"workdir":"/tmp/task","task_packet":"Inspect fixture","skill":"unknown"}`))
	if err != nil {
		t.Fatal(err)
	}
	// No runtime or process leaser exists: resolving an unknown skill must fail
	// before any installed-worker access or launch can take place.
	result, err := r.workerOperation(t.Context(), "worker_start", "turn", ledger.Event{}, in)
	if err != skills.ErrNotFound || result.State != tools.Failed {
		t.Fatalf("unknown method: %+v %v", result, err)
	}
}

type workerProbe struct {
	worker.Adapter
	snapshot           worker.ThreadSnapshot
	inputs, interrupts int
}

func (p *workerProbe) Snapshot(context.Context, worker.Thread) (worker.ThreadSnapshot, error) {
	return p.snapshot, nil
}
func (p *workerProbe) Input(context.Context, worker.Thread, worker.Input) error {
	p.inputs++
	return nil
}
func (p *workerProbe) Interrupt(context.Context, worker.Thread, worker.Reason) error {
	p.interrupts++
	return nil
}

func TestWorkerControlsAreBoundToConversationAndCurrentHandle(t *testing.T) {
	owner := &conversationRuntime{person: "person", agent: "agent", conversation: "main:agent"}
	probe := &workerProbe{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn", ProcessGeneration: 7, BindingEpoch: 2}, State: worker.Active}}
	entry := &workerSession{adapter: probe, origin: owner, thread: probe.snapshot.Thread, active: map[string]lease.Lease{}}
	owner.workers = &workerRuntime{sessions: map[string]*workerSession{"thread": entry}}
	good := workerArgs{Session: "thread", Turn: "turn", Generation: 7, Binding: 2, Text: "steer"}
	run := func(r *conversationRuntime, args workerArgs) tools.Result {
		raw, _ := json.Marshal(args)
		in, err := workerSchema("worker_input", raw)
		if err != nil {
			t.Fatal(err)
		}
		result, err := r.workerOperation(t.Context(), "worker_input", "conversation-turn", ledger.Event{}, in)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	foreign := &conversationRuntime{person: owner.person, agent: "other", conversation: "main:other", workers: owner.workers}
	if result := run(foreign, good); result.State != tools.Failed {
		t.Fatal("foreign conversation accepted")
	}
	for _, change := range []func(*workerArgs){func(a *workerArgs) { a.Turn = "stale" }, func(a *workerArgs) { a.Generation++ }, func(a *workerArgs) { a.Binding++ }} {
		stale := good
		change(&stale)
		if result := run(owner, stale); result.State != tools.Failed {
			t.Fatal("stale control accepted")
		}
	}
	if probe.inputs != 0 {
		t.Fatal("refused input reached worker")
	}
	if result := run(owner, good); result.State != tools.Applied || probe.inputs != 1 {
		t.Fatalf("current control refused: %+v", result)
	}
}

func TestWorkerInterruptRequiresQuoteFromCurrentUserInput(t *testing.T) {
	r := &conversationRuntime{workers: &workerRuntime{}}
	bindings := map[string]loop.Tool{}
	if err := r.prepareWorkerTools("turn", ledger.Event{}, bindings); err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: "cancel", Name: "worker_interrupt", Arguments: `{"session":"thread","turn":"turn","generation":1,"binding":1,"quote":"stop this worker"}`}
	if result := bindings[call.Name].Execute(t.Context(), call); result.Code != loop.Denied {
		t.Fatalf("unsourced cancel accepted: %+v", result)
	}
}

func TestWorkerStartAcceptsSingleMethodSelection(t *testing.T) {
	if _, err := workerSchema("worker_start", json.RawMessage(`{"workdir":"/tmp/task","task_packet":"Inspect fixture","kind":"post_merge_qa","skill":"qa"}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"workdir":"/tmp/task","task_packet":"Inspect fixture","kind":"unrecognized"}`,
		`{"workdir":"/tmp/task","task_packet":"Inspect fixture","skill":["qa","perf"]}`,
		`{"workdir":"/tmp/task","task_packet":"Inspect fixture","skill":"../qa"}`,
	} {
		if _, err := workerSchema("worker_start", json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
