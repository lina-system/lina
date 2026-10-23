// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestWorkerStartCancelledDuringManagerAdmissionCanRetry(t *testing.T) {
	r := controlRuntime(t)
	first, err := r.agents.FirstRun(t.Context(), r.person, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.agents.Create(t.Context(), agents.Create{PersonID: r.person, Name: "Dev", Preset: "dev"}); err != nil {
		t.Fatal(err)
	}
	r = newConversationView(r, first.Agent.ID, "main:"+first.Agent.ID)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(workerArgs{Workdir: dir, TaskPacket: "Inspect fixture"})
	if err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: "start", Name: "worker_start", Arguments: string(raw)}
	in, err := workerSchema(call.Name, json.RawMessage(call.Arguments))
	if err != nil {
		t.Fatal(err)
	}
	const turnID = "admission-turn"
	a, err := r.assignCoding(t.Context(), dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: turnID}, codingID(turnID, call.Name, string(in.Arguments)), call.Name, dir, in.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	// Occupy the repository's manager lane until the caller has cancelled.
	busy, release := make(chan struct{}), make(chan struct{})
	active, err := r.lanes.Submit(t.Context(), a.Conversation, func(context.Context) error { close(busy); <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); <-active }()
	<-busy
	bindings := map[string]loop.Tool{}
	if _, err = r.prepareWorkerTools(turnID, &toolPreparation{event: ledger.Event{}}, bindings); err != nil {
		t.Fatal(err)
	}
	cancelled, cancelBeforeAdmission := context.WithCancel(t.Context())
	cancelBeforeAdmission()
	if result := bindings[call.Name].Execute(cancelled, call); result.Code != loop.Aborted {
		t.Fatalf("cancelled caller entered admission: %+v", result)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan loop.Result, 1)
	go func() { done <- bindings[call.Name].Execute(ctx, call) }()
	// Wait for the durable handoff, not for a guessed scheduling delay.
	wait, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	for {
		var count int
		err = r.db.Read(wait, func(tx *canon.Tx) error {
			return tx.QueryRowContext(wait, "SELECT count(*) FROM conv_event WHERE id=?", "coding-assignment:"+a.ID).Scan(&count)
		})
		if err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("admission did not reach manager: %+v", result)
		case <-wait.Done():
			t.Fatal(wait.Err())
		case <-time.After(time.Millisecond):
		}
	}
	key := turn.EffectKey(turnID, call.Name, in.Target, call.ID)
	_, beforeErr := r.personaAuthority.Effect(t.Context(), key)
	cancel()
	result := <-done
	if !errors.Is(beforeErr, sql.ErrNoRows) || result.Code != loop.Aborted {
		t.Fatalf("manager wait consumed worker effect: lookup=%v result=%+v", beforeErr, result)
	}
	if _, err = r.personaAuthority.Effect(t.Context(), key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cancelled admission recorded a start: %v", err)
	}
	var reservations int
	if err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_worker_reservations").Scan(&reservations)
	}); err != nil || reservations != 0 {
		t.Fatalf("cancelled admission retained directory reservations: %d %v", reservations, err)
	}
	// A later durable manager decision permits the identical request to reach
	// dispatch. A closed runtime makes that attempt fail before spawning.
	a.Delegated = true
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error { return saveCoding(t.Context(), tx, a) }); err != nil {
		t.Fatal(err)
	}
	r.workers.mu.Lock()
	r.workers.closed = true
	r.workers.mu.Unlock()
	result = bindings[call.Name].Execute(t.Context(), call)
	effect, err := r.personaAuthority.Effect(t.Context(), key)
	if err != nil || result.Code != loop.Failed || effect.State != tools.Failed {
		t.Fatalf("identical retry did not reach start: result=%+v effect=%+v err=%v", result, effect, err)
	}
	// A later ownership transfer must not turn a known receipt replay into
	// another manager admission or execution attempt.
	if err := r.work.Create(t.Context(), work.Task{ID: "external", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Transferred work", Done: "Result", ExternalControl: true, ArtifactRoots: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	replayed := bindings[call.Name].Execute(t.Context(), call)
	priorJSON, err := json.Marshal(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	replayedJSON, err := json.Marshal(replayed.Output)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != result.ID || replayed.Code != result.Code || string(replayedJSON) != string(priorJSON) {
		t.Fatalf("known receipt re-entered admission: prior=%s replayed=%s", priorJSON, replayedJSON)
	}
}

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

func TestWorkerStartValidatesCallBeforeManagerAdmission(t *testing.T) {
	r := controlRuntime(t)
	bindings := map[string]loop.Tool{}
	if _, err := r.prepareWorkerTools("turn", &toolPreparation{event: ledger.Event{}}, bindings); err != nil {
		t.Fatal(err)
	}
	for _, call := range []provider.ToolCall{
		{Name: "worker_start", Arguments: `{"workdir":"/tmp/synthetic","task_packet":"Inspect fixture"}`},
		{ID: "start", Name: "other", Arguments: `{"workdir":"/tmp/synthetic","task_packet":"Inspect fixture"}`},
	} {
		if result := bindings["worker_start"].Execute(t.Context(), call); result.Code != loop.Invalid {
			t.Fatalf("invalid call reached admission: %+v", result)
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
	if _, err := r.prepareWorkerTools("turn", &toolPreparation{event: ledger.Event{}}, bindings); err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: "cancel", Name: "worker_interrupt", Arguments: `{"session":"thread","turn":"turn","generation":1,"binding":1,"quote":"stop this worker"}`}
	if result := bindings[call.Name].Execute(t.Context(), call); result.Code != loop.Denied {
		t.Fatalf("unsourced cancel accepted: %+v", result)
	}
}

func TestScheduledSessionCannotBypassSchedulerWithOrdinaryInput(t *testing.T) {
	owner := &conversationRuntime{person: "person", agent: "agent", conversation: "main:agent"}
	probe := &workerProbe{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}, State: worker.Idle}}
	entry := &workerSession{adapter: probe, origin: owner, thread: probe.snapshot.Thread, scheduled: true}
	owner.workers = &workerRuntime{sessions: map[string]*workerSession{"thread": entry}}
	args, _ := json.Marshal(workerArgs{Session: "thread", Turn: "turn", Generation: 1, Binding: 1, Text: "continue"})
	in, err := workerSchema("worker_input", args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.workerOperation(t.Context(), "worker_input", "turn", ledger.Event{}, in)
	if err != nil || result.State != tools.Failed || probe.inputs != 0 {
		t.Fatalf("scheduled task bypassed admission: %+v inputs=%d error=%v", result, probe.inputs, err)
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
