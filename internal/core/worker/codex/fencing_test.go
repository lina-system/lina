// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestLN133AnswersAreSingleUseAndInvalidAnswersStayPending(t *testing.T) {
	p := newAdapterPeer(t, `{"rateLimits":{}}`)
	th := p.start(t)
	p.send(requestFrame(`"approval"`, "item/commandExecution/requestApproval", approvalParams))
	e := p.event(t, worker.ApprovalEvent)
	for _, result := range []string{`null`, `{}`, `{"decision":"future"}`, `{"decision":true}`, `{"decision":{"acceptWithExecpolicyAmendment":{}}}`} {
		if err := p.s.Answer(p.ctx, e.Thread, worker.Answer{RequestID: e.Approval.ID, Result: json.RawMessage(result)}); err == nil {
			t.Fatalf("invalid response accepted: %s", result)
		}
	}
	canceled, cancel := context.WithCancel(p.ctx)
	cancel()
	a := worker.Answer{RequestID: e.Approval.ID, Result: json.RawMessage(`{"decision":"decline"}`)}
	if err := p.s.Answer(canceled, e.Thread, a); err != context.Canceled {
		t.Fatalf("canceled answer=%v", err)
	}
	if snap := p.snapshot(t, th); len(snap.Pending) != 1 {
		t.Fatal("invalid or canceled answer consumed pending")
	}
	var wg sync.WaitGroup
	answers := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); answers <- p.s.Answer(p.ctx, e.Thread, a) }()
	}
	wg.Wait()
	successes := 0
	for i := 0; i < 2; i++ {
		if <-answers == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful answers=%d", successes)
	}
	reply := p.receivedMethod(t, "")
	if string(reply.ID) != `"approval"` {
		t.Fatalf("wire ID=%s", reply.ID)
	}
	if err := p.s.Input(p.ctx, th, worker.Input{Text: "continue"}); err != nil {
		t.Fatal(err)
	}
	p.receivedMethod(t, "turn/steer") // Orders all earlier wire frames before inspection.
	select {
	case extra := <-p.received:
		t.Fatalf("extra wire answer: %+v", extra)
	default:
	}
}

func TestLN133ResolvedProgressAndPendingCopy(t *testing.T) {
	p := newAdapterPeer(t, `{"rateLimits":{}}`)
	th := p.start(t)
	p.send(requestFrame(`"question"`, "item/tool/requestUserInput", `{"threadId":"thread","turnId":"turn-1","itemId":"input","isBlocking":true,"questions":[{"id":"q","header":"Choose","question":"Which?"}]}`))
	e := p.event(t, worker.QuestionEvent)
	snap := p.snapshot(t, th)
	snap.Pending[0].Params[0] = '!'
	snap.Pending[0].ID = "modified"
	fresh := p.snapshot(t, th)
	if fresh.Pending[0].ID != e.Question.ID || fresh.Pending[0].Params[0] != '{' {
		t.Fatal("snapshot aliases pending")
	}
	p.send(frame{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"thread","requestId":"question"}`)})
	for {
		event := p.event(t, worker.ProgressEvent)
		if event.Progress.Phase == "server_request_resolved" {
			if event.Progress.ItemID != e.Question.ID {
				t.Fatal("resolution lost opaque id")
			}
			break
		}
	}
	if err := p.s.Input(p.ctx, th, worker.Input{Text: "continue"}); err != nil {
		t.Fatal(err)
	}
}

func TestLN133RequestRejections(t *testing.T) {
	for _, tc := range []struct{ name, id, method, params string }{
		{"wrong thread", `1`, "item/commandExecution/requestApproval", `{"threadId":"foreign","turnId":"turn-1","itemId":"item","startedAtMs":1}`},
		{"wrong turn", `1`, "item/commandExecution/requestApproval", `{"threadId":"thread","turnId":"foreign","itemId":"item","startedAtMs":1}`},
		{"null id", `null`, "item/commandExecution/requestApproval", approvalParams},
		{"fractional id", `1.5`, "item/commandExecution/requestApproval", approvalParams},
		{"unknown method", `1`, "future/request", approvalParams},
		{"undeclared tool", `1`, "item/tool/call", `{"threadId":"thread","turnId":"turn-1","namespace":"lina","tool":"undeclared","callId":"call","arguments":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newAdapterPeer(t, `{"rateLimits":{}}`)
			p.start(t)
			p.send(requestFrame(tc.id, tc.method, tc.params))
			<-p.s.readerDone
			p.s.mu.Lock()
			failure := p.s.failure
			p.s.mu.Unlock()
			if failure == nil {
				t.Fatal("request was not rejected")
			}
			for e := range p.s.Events() {
				if e.Approval != nil || e.Question != nil || e.ToolCall != nil {
					t.Fatalf("foreign request published: %+v", e)
				}
			}
		})
	}
}

func TestLN133DuplicateRequestIsNotRepublished(t *testing.T) {
	p := newAdapterPeer(t, `{"rateLimits":{}}`)
	p.start(t)
	r := requestFrame(`1`, "item/commandExecution/requestApproval", approvalParams)
	p.send(r)
	e := p.event(t, worker.ApprovalEvent)
	p.send(r)
	<-p.s.readerDone
	for next := range p.s.Events() {
		if next.Approval != nil {
			t.Fatal("duplicate approval published")
		}
	}
	if err := p.s.Answer(context.Background(), e.Thread, worker.Answer{RequestID: e.Approval.ID, Result: json.RawMessage(`{"decision":"accept"}`)}); err == nil {
		t.Fatal("failed session accepted answer")
	}
}

func TestLN133OldRequestCannotAnswerReusedWireID(t *testing.T) {
	p := newAdapterPeer(t, `{"rateLimits":{}}`)
	th := p.start(t)
	p.send(requestFrame(`1`, "item/commandExecution/requestApproval", approvalParams))
	old := p.event(t, worker.ApprovalEvent)
	p.send(frame{Method: "turn/started", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn-2","status":"inProgress"}}`)})
	next := requestFrame(`1`, "item/commandExecution/requestApproval", `{"threadId":"thread","turnId":"turn-2","itemId":"item","startedAtMs":1}`)
	p.send(next)
	current := p.event(t, worker.ApprovalEvent)
	if current.Approval.ID == old.Approval.ID {
		t.Fatal("opaque request ID reused")
	}
	for _, binding := range []worker.Thread{old.Thread, current.Thread} {
		if err := p.s.Answer(p.ctx, binding, worker.Answer{RequestID: old.Approval.ID, Result: json.RawMessage(`{"decision":"accept"}`)}); err == nil {
			t.Fatal("stale request answered")
		}
	}
	_ = th
	if err := p.s.Answer(p.ctx, current.Thread, worker.Answer{RequestID: current.Approval.ID, Result: json.RawMessage(`{"decision":"decline"}`)}); err != nil {
		t.Fatal(err)
	}
}
