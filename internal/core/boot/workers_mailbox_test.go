// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/worker"
)

type mailboxProbe struct {
	workerProbe
	tier      string
	replies   []json.RawMessage
	events    chan worker.Event
	delivered chan struct{}
}

func (p *mailboxProbe) Name() string { return "fake" }
func (p *mailboxProbe) DeliveryProfile() worker.DeliveryProfile {
	return worker.DeliveryProfile{Tier: p.tier, Mechanism: "fixture", Available: true}
}
func (p *mailboxProbe) Steer(ctx context.Context, th worker.Thread, in worker.Input) error {
	return p.Input(ctx, th, in)
}
func (p *mailboxProbe) Events() <-chan worker.Event { return p.events }
func (p *mailboxProbe) Input(ctx context.Context, th worker.Thread, in worker.Input) error {
	err := p.workerProbe.Input(ctx, th, in)
	if p.delivered != nil {
		select {
		case p.delivered <- struct{}{}:
		default:
		}
	}
	return err
}
func (p *mailboxProbe) Answer(_ context.Context, _ worker.Thread, a worker.Answer) error {
	p.replies = append(p.replies, a.Result)
	return nil
}

func TestWorkerMailboxTiers(t *testing.T) {
	for _, tier := range []string{"push", "turn", "pull"} {
		t.Run(tier, func(t *testing.T) {
			r := controlRuntime(t)
			th := worker.Thread{ID: "session", TaskID: "task", AttemptID: "attempt", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
			p := &mailboxProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: th, State: worker.Active}}, tier: tier}
			e := &workerSession{origin: r, thread: th, adapter: p, scheduled: true}
			r.workers.sessions[th.ID] = e
			t.Cleanup(func() { delete(r.workers.sessions, th.ID) })
			for range 2 {
				if err := r.ownerWorkerControl(t.Context(), clientsrv.Session{PersonID: r.person}, "session:"+th.ID, nil, "message", "write a result"); err != nil {
					t.Fatal(err)
				}
			}
			messages, err := r.work.SteeringMessages(t.Context(), "fake:session")
			if err != nil || len(messages) != 1 {
				t.Fatalf("missing mailbox receipt: %+v %v", messages, err)
			}
			if tier == "push" && p.inputs != 1 || tier != "push" && p.inputs != 0 {
				t.Fatal("incorrect delivery boundary", p.inputs)
			}
			event := worker.Event{Thread: th, Kind: worker.ToolCallEvent, ToolCall: &worker.ToolCall{ID: "call", Namespace: "lina", Name: "read_projection", Arguments: json.RawMessage(`{}`)}}
			e.workTool(event)
			messages, err = r.work.SteeringMessages(t.Context(), "fake:session")
			if err != nil {
				t.Fatal(err)
			}
			if tier == "pull" {
				if messages[0].State != "stored" {
					t.Fatal("pull was delivered without check_inbox", messages)
				}
				event.ToolCall.Name = "check_inbox"
				e.workTool(event)
			}
			messages, _ = r.work.SteeringMessages(t.Context(), "fake:session")
			if messages[0].State != "delivered" {
				t.Fatal("message not delivered", messages)
			}
			event.ToolCall.Name = "check_inbox"
			event.ToolCall.Arguments, _ = json.Marshal(map[string]any{"read": []string{messages[0].ID}, "acted": []string{messages[0].ID}})
			e.workTool(event)
			messages, _ = r.work.SteeringMessages(t.Context(), "fake:session")
			if messages[0].State != "read" || messages[0].ActionLatencyMS == nil {
				t.Fatal("missing read/action receipt", messages)
			}
			t.Logf("%s send-to-action: %d ms (fake worker acknowledgement)", tier, *messages[0].ActionLatencyMS)
		})
	}
}

func TestWorkerMailboxDownResumeOnce(t *testing.T) {
	r := controlRuntime(t)
	th := worker.Thread{ID: "session", TaskID: "task", AttemptID: "attempt", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
	p := &mailboxProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: th, State: worker.Dead}}, tier: "push"}
	e := &workerSession{origin: r, thread: th, adapter: p, scheduled: true}
	r.workers.sessions[th.ID] = e
	t.Cleanup(func() { delete(r.workers.sessions, th.ID) })
	if err := r.ownerWorkerControl(t.Context(), clientsrv.Session{PersonID: r.person}, "session:"+th.ID, nil, "offline", "write after resume"); err != nil {
		t.Fatal(err)
	}
	messages, _ := r.work.SteeringMessages(t.Context(), "fake:session")
	if len(messages) != 1 || messages[0].State != "held" || p.inputs != 0 {
		t.Fatal("down session received input", messages, p.inputs)
	}
	// Replace the binding, as restart does, while keeping only durable mail.
	resumed := &workerSession{origin: r, thread: th, adapter: p, scheduled: true}
	p.snapshot.State = worker.Active
	for range 2 {
		if err := resumed.resumeSteering(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if p.inputs != 1 {
		t.Fatal("resume did not deliver exactly once", p.inputs)
	}
	messages, _ = r.work.SteeringMessages(t.Context(), "fake:session")
	if messages[0].State != "delivered" {
		t.Fatal(messages)
	}
}

func TestWorkerMailboxGoneRefusesAndForeignCannotRead(t *testing.T) {
	r := controlRuntime(t)
	th := worker.Thread{ID: "session", TaskID: "task", AttemptID: "attempt", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
	p := &mailboxProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: th, State: worker.Active}}, tier: "pull"}
	e := &workerSession{origin: r, thread: th, adapter: p, scheduled: true}
	if _, err := e.storeSteering(t.Context(), "one", "pending"); err != nil {
		t.Fatal(err)
	}
	if err := e.finishSteering(t.Context(), worker.Event{Thread: th, Finish: &worker.Finish{Status: worker.Done}}); err != nil {
		t.Fatal(err)
	}
	m, _ := r.work.SteeringMessages(t.Context(), "fake:session")
	if m[0].State != "refused" || m[0].Reason != "session_gone" {
		t.Fatal(m)
	}
	event := worker.Event{Thread: th, ToolCall: &worker.ToolCall{Namespace: "lina", Name: "check_inbox", Arguments: json.RawMessage(`{}`)}}
	event.Thread.ProcessGeneration++
	if _, err := e.executeWorkTool(t.Context(), event); err == nil {
		t.Fatal("foreign binding read inbox")
	}
}

func TestWorkerMailboxOfflineAddressAndUncertainDelivery(t *testing.T) {
	r := controlRuntime(t)
	th := worker.Thread{ID: "offline", TaskID: "task", AttemptID: "attempt", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
	rec := recovery.Record{ID: th.ID, Input: "source", Worker: "fake", Person: r.person, Agent: r.agent, Conversation: r.conversation, Thread: th, Workdir: t.TempDir(), State: "unknown"}
	if err := (recovery.Recovery{DB: r.db}).Put(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := r.ownerWorkerControl(t.Context(), clientsrv.Session{PersonID: r.person}, "session:"+th.ID, nil, "down", "resume mail"); err != nil {
		t.Fatal(err)
	}
	if err := r.ownerWorkerControl(t.Context(), clientsrv.Session{PersonID: "foreign"}, "session:"+th.ID, nil, "foreign", "wrong mail"); err == nil {
		t.Fatal("foreign owner queued mail")
	}
	p := &mailboxProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: th, State: worker.Active}}, tier: "push"}
	e := &workerSession{origin: r, thread: th, adapter: p, scheduled: true}
	if err := e.resumeSteering(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.resumeSteering(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.inputs != 1 {
		t.Fatal("offline mail replayed", p.inputs)
	}
	m, err := e.storeSteering(t.Context(), "uncertain", "must not repeat")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.work.SteeringState(t.Context(), m.Session, m.ID, "held", "delivery_uncertain", time.Now()); err != nil {
		t.Fatal(err)
	}
	p.snapshot.State = worker.Dead
	if err = e.ownerSteering(t.Context(), "uncertain", "must not repeat"); err == nil {
		t.Fatal("uncertain delivery silently accepted")
	}
	p.snapshot.State = worker.Active
	if err = e.resumeSteering(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.inputs != 1 {
		t.Fatal("uncertain message resent", p.inputs)
	}
}

func TestWorkerMailboxHoldsInputUntilNativeDecisionResolves(t *testing.T) {
	r := controlRuntime(t)
	th := worker.Thread{ID: "waiting", TaskID: "task", AttemptID: "attempt", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
	p := &mailboxProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: th, State: worker.Waiting, Pending: []worker.Request{{ID: "approval", Method: "session/request_permission"}}}}, tier: "turn"}
	e := &workerSession{origin: r, thread: th, adapter: p, scheduled: true}
	if err := e.ownerSteering(t.Context(), "waiting-message", "follow-up after the decision"); err != nil {
		t.Fatal(err)
	}
	event := worker.Event{Thread: th, Kind: worker.ToolCallEvent, ToolCall: &worker.ToolCall{ID: "tool", Namespace: "lina", Name: "read_projection", Arguments: json.RawMessage(`{}`)}}
	e.workTool(event)
	messages, err := r.work.SteeringMessages(t.Context(), "fake:waiting")
	if err != nil || len(messages) != 1 || messages[0].State != "held" {
		t.Fatalf("ordinary input bypassed native decision: %+v %v", messages, err)
	}
	p.snapshot.State, p.snapshot.Pending = worker.Active, nil
	e.workTool(event)
	messages, err = r.work.SteeringMessages(t.Context(), "fake:waiting")
	if err != nil || messages[0].State != "delivered" {
		t.Fatalf("resolved decision did not release mail: %+v %v", messages, err)
	}
}

func TestWorkerMailboxPushAfterAnswer(t *testing.T) {
	r := controlRuntime(t)
	th := worker.Thread{ID: "answer", TaskID: "task", AttemptID: "attempt", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
	p := &mailboxProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: th, State: worker.Waiting}}, tier: "push"}
	e := &workerSession{origin: r, thread: th, adapter: p, scheduled: true, done: make(chan struct{}), close: func() error { return nil }}
	if err := e.ownerSteering(t.Context(), "after-answer", "continue with the correction"); err != nil {
		t.Fatal(err)
	}
	p.snapshot.State = worker.Active
	e.answerDelivered(t.Context(), th)
	e.answerDelivered(t.Context(), th)
	if p.inputs != 1 {
		t.Fatalf("answer did not deliver held push once: %d", p.inputs)
	}
}

func TestWorkerMailboxPushOnResolvedEvent(t *testing.T) {
	r := controlRuntime(t)
	th := worker.Thread{ID: "resolved", TaskID: "task", AttemptID: "attempt", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
	p := &mailboxProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: th, State: worker.Waiting, Pending: []worker.Request{{ID: "approval", Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{}`)}}}}, tier: "push", events: make(chan worker.Event)}
	e := &workerSession{origin: r, thread: th, adapter: p, scheduled: true, done: make(chan struct{}), close: func() error { return nil }, approvals: map[string]worker.Event{}, questions: map[string]*workerQuestion{}}
	if err := e.ownerSteering(t.Context(), "while-waiting", "apply the correction"); err != nil {
		t.Fatal(err)
	}
	go e.observe()
	p.snapshot.State, p.snapshot.Pending = worker.Active, nil
	p.events <- worker.Event{Thread: th, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "server_request_resolved", ItemID: "approval"}}
	// The second send returns only after the resolved event has been handled.
	p.events <- worker.Event{Thread: th, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "barrier"}}
	close(p.events)
	<-e.done
	if p.inputs != 1 {
		t.Fatalf("resolved event delivered push mail %d times", p.inputs)
	}
}

func TestOfflineMailboxUsesConnectionTier(t *testing.T) {
	for name, tier := range map[string]string{"codex": "push", "gemini": "turn", "grok": "turn", "cursor": "turn", "claude-acp": "turn", "custom": "pull"} {
		if profile := offlineDelivery(name); profile.Tier != tier {
			t.Errorf("%s: %+v", name, profile)
		}
	}
}
