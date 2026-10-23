// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
)

type adapterPeer struct {
	s        *Session
	ctx      context.Context
	send     func(frame)
	received chan frame
}

// Real pipes exercise the transport; the peer speaks only synthetic protocol data.
func newAdapterPeer(t *testing.T, quota string, beforeTurnReply ...frame) *adapterPeer {
	t.Helper()
	return newAdapterPeerScript(t, quota, beforeTurnReply, nil)
}

func newAdapterPeerScript(t *testing.T, quota string, beforeTurnReply, beforeQuotaReply []frame) *adapterPeer {
	return newAdapterPeerTurnScript(t, quota, beforeTurnReply, beforeQuotaReply, true)
}

func newAdapterPeerTurnScript(t *testing.T, quota string, beforeTurnReply, beforeQuotaReply []frame, announceTurn bool) *adapterPeer {
	t.Helper()
	return newAdapterPeerTurnScript(t, quota, beforeTurnReply, beforeQuotaReply, true)
}

func newAdapterPeerTurnScript(t *testing.T, quota string, beforeTurnReply, beforeQuotaReply []frame, announceTurn bool) *adapterPeer {
	t.Helper()
	return newAdapterPeerTurnScript(t, quota, beforeTurnReply, beforeQuotaReply, true)
}

func newAdapterPeerTurnScript(t *testing.T, quota string, beforeTurnReply, beforeQuotaReply []frame, announceTurn bool) *adapterPeer {
	t.Helper()
	return newAdapterPeerTurnScript(t, quota, beforeTurnReply, beforeQuotaReply, true)
}

func newAdapterPeerTurnScript(t *testing.T, quota string, beforeTurnReply, beforeQuotaReply []frame, announceTurn bool) *adapterPeer {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	s := &Session{stdin: inW, cancel: cancel, readerDone: make(chan struct{}), events: make(chan worker.Event, 257), pending: map[string]chan frame{}, finished: map[string]bool{}, generation: generations.Add(1), version: "fixture"}
	p := &adapterPeer{s: s, ctx: ctx, received: make(chan frame, 128)}
	var write sync.Mutex
	p.send = func(f frame) {
		write.Lock()
		defer write.Unlock()
		if err := json.NewEncoder(outW).Encode(f); err != nil && ctx.Err() == nil {
			t.Errorf("peer write: %v", err)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		scan := bufio.NewScanner(inR)
		turn := 0
		for scan.Scan() {
			var f frame
			if err := json.Unmarshal(scan.Bytes(), &f); err != nil {
				t.Error(err)
				return
			}
			p.received <- f
			var result any
			switch f.Method {
			case "thread/start":
				var params struct{ Cwd string }
				json.Unmarshal(f.Params, &params)
				result = map[string]any{"thread": map[string]any{"id": "thread"}, "cwd": params.Cwd, "model": "own"}
			case "turn/start":
				turn++
				if announceTurn {
					p.send(frame{Method: "turn/started", Params: json.RawMessage(fmt.Sprintf(`{"threadId":"thread","turn":{"id":"turn-%d","status":"inProgress"}}`, turn))})
				}
				for _, early := range beforeTurnReply {
					p.send(early)
				}
				result = map[string]any{"turn": map[string]any{"id": fmt.Sprintf("turn-%d", turn)}}
			case "thread/read":
				result = map[string]any{"thread": map[string]any{"id": "thread", "status": map[string]any{"type": "active", "activeFlags": []string{}}, "turns": []any{map[string]any{"id": fmt.Sprintf("turn-%d", turn), "status": "inProgress"}}}}
			case "turn/steer":
				result = map[string]any{"turnId": fmt.Sprintf("turn-%d", turn)}
			case "account/rateLimits/read":
				for _, update := range beforeQuotaReply {
					p.send(update)
				}
				result = json.RawMessage(quota)
			case "":
				continue
			default:
				t.Errorf("unexpected client method %s", f.Method)
				return
			}
			raw, _ := json.Marshal(result)
			p.send(frame{ID: f.ID, Result: raw})
		}
	}()
	go s.read(outR)
	t.Cleanup(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		cancel()
		inW.Close()
		outW.Close()
		<-s.readerDone
		inR.Close()
		<-done
	})
	return p
}

func (p *adapterPeer) start(t *testing.T, tools ...worker.Tool) worker.Thread {
	t.Helper()
	l := lease.New(func() {})
	t.Cleanup(l.Release)
	th, err := p.s.Start(p.ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: t.TempDir(), TaskPacket: "task", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	return th
}
func (p *adapterPeer) event(t *testing.T, kind worker.EventKind) worker.Event {
	t.Helper()
	for {
		select {
		case e, ok := <-p.s.Events():
			if !ok {
				p.s.mu.Lock()
				err := p.s.failure
				p.s.mu.Unlock()
				t.Fatalf("events ended: %v", err)
			}
			if e.Kind == worker.FinishEvent {
				t.Fatalf("unexpected finish: %+v", e.Finish)
			}
			if e.Kind == kind {
				if err := e.Validate(); err != nil {
					t.Fatal(err)
				}
				return e
			}
		case <-p.ctx.Done():
			t.Fatal(p.ctx.Err())
		}
	}
}
func (p *adapterPeer) receivedMethod(t *testing.T, method string) frame {
	t.Helper()
	for {
		select {
		case f := <-p.received:
			if f.Method == method {
				return f
			}
		case <-p.ctx.Done():
			t.Fatal(p.ctx.Err())
		}
	}
}
func (p *adapterPeer) snapshot(t *testing.T, th worker.Thread) worker.ThreadSnapshot {
	t.Helper()
	snap, err := p.s.Snapshot(p.ctx, th)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
func requestFrame(id, method, params string) frame {
	return frame{ID: json.RawMessage(id), Method: method, Params: json.RawMessage(params)}
}

const approvalParams = `{"threadId":"thread","turnId":"turn-1","itemId":"item","startedAtMs":1,"command":"echo synthetic"}`

func TestLN133DynamicToolsAndCorrelation(t *testing.T) {
	p := newAdapterPeer(t, `{"rateLimits":{}}`)
	th := p.start(t, worker.Tool{Namespace: "lina", Name: "report", Description: "Report", InputSchema: json.RawMessage(`{"type":"object"}`)})
	if err := p.s.Capabilities().Require(worker.WorkTools, worker.AskQuestions, worker.RequestApprovals, worker.ReportQuota); err != nil {
		t.Fatal(err)
	}
	start := p.receivedMethod(t, "thread/start")
	var params map[string]json.RawMessage
	json.Unmarshal(start.Params, &params)
	var tools []struct {
		Type, Name string
		Tools      []struct {
			Type, Name, Description string
			InputSchema             json.RawMessage
		}
	}
	if err := json.Unmarshal(params["dynamicTools"], &tools); err != nil {
		t.Fatal(err)
	}
	if len(params) != 2 || len(tools) != 1 || tools[0].Type != "namespace" || tools[0].Name != "lina" || len(tools[0].Tools) != 1 || tools[0].Tools[0].Name != "report" || tools[0].Tools[0].Type != "function" {
		t.Fatalf("tools=%s", start.Params)
	}
	p.send(requestFrame(`"rpc"`, "item/tool/call", `{"threadId":"thread","turnId":"turn-1","callId":"wire-call","namespace":"lina","tool":"report","arguments":{"result":"ok"}}`))
	e := p.event(t, worker.ToolCallEvent)
	if e.ToolCall.CallID != "wire-call" || e.ToolCall.ID == "wire-call" || e.ToolCall.Namespace != "lina" || e.ToolCall.Name != "report" {
		t.Fatalf("call=%+v", e.ToolCall)
	}
	snap := p.snapshot(t, th)
	if len(snap.Pending) != 1 || snap.Pending[0].ID != e.ToolCall.ID {
		t.Fatalf("pending=%+v", snap.Pending)
	}
	var raw map[string]any
	json.Unmarshal(snap.Pending[0].Params, &raw)
	if raw["callId"] != "wire-call" {
		t.Fatal("wire callId lost")
	}
	result := json.RawMessage(`{"contentItems":[{"type":"inputText","text":"ok"}],"success":true}`)
	if err := p.s.Answer(p.ctx, e.Thread, worker.Answer{RequestID: e.ToolCall.ID, Result: result}); err != nil {
		t.Fatal(err)
	}
	reply := p.receivedMethod(t, "")
	if string(reply.ID) != `"rpc"` || !reflect.DeepEqual(reply.Result, result) {
		t.Fatalf("reply=%s %s", reply.ID, reply.Result)
	}
	if err := p.s.Answer(p.ctx, e.Thread, worker.Answer{RequestID: e.ToolCall.ID, Result: result}); err == nil {
		t.Fatal("duplicate answer accepted")
	}
}

func TestLN133ServerRequests(t *testing.T) {
	for _, tc := range []struct {
		method, params, result string
		kind                   worker.EventKind
	}{
		{"item/commandExecution/requestApproval", approvalParams, `{"decision":"decline"}`, worker.ApprovalEvent},
		{"item/fileChange/requestApproval", `{"threadId":"thread","turnId":"turn-1","itemId":"file","startedAtMs":1}`, `{"decision":"accept"}`, worker.ApprovalEvent},
		{"item/permissions/requestApproval", `{"threadId":"thread","turnId":"turn-1","itemId":"permissions","startedAtMs":1,"cwd":"/tmp","permissions":{"network":{"enabled":true}}}`, `{"permissions":{},"scope":"turn"}`, worker.ApprovalEvent},
		{"item/tool/requestUserInput", `{"threadId":"thread","turnId":"turn-1","itemId":"input","isBlocking":true,"questions":[{"id":"q","header":"Choose","question":"Which?"}]}`, `{"answers":{"q":{"answers":["A"]}}}`, worker.QuestionEvent},
		{"mcpServer/elicitation/request", `{"threadId":"thread","turnId":"turn-1","serverName":"fixture","mode":"form","message":"synthetic","requestedSchema":{"type":"object","properties":{}}}`, `{"action":"decline"}`, worker.QuestionEvent},
		{"mcpServer/elicitation/request", `{"threadId":"thread","serverName":"fixture","mode":"url","message":"synthetic","url":"https://example.invalid","elicitationId":"elicit"}`, `{"action":"cancel"}`, worker.QuestionEvent},
	} {
		t.Run(tc.method+tc.result, func(t *testing.T) {
			p := newAdapterPeer(t, `{"rateLimits":{}}`)
			th := p.start(t)
			p.send(requestFrame(`9007199254740993`, tc.method, tc.params))
			e := p.event(t, tc.kind)
			r := e.Approval
			if r == nil {
				r = e.Question
			}
			if r.Method != tc.method || string(r.Params) != tc.params {
				t.Fatalf("request changed: %+v", r)
			}
			p.send(frame{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"thread","status":{"type":"active","activeFlags":[]}}`)})
			snap := p.snapshot(t, th)
			if snap.State != worker.Waiting || len(snap.Pending) != 1 {
				t.Fatalf("snapshot=%+v", snap)
			}
			if err := p.s.Input(p.ctx, th, worker.Input{Text: "blocked"}); err == nil {
				t.Fatal("pending input accepted")
			}
			for _, mutate := range []func(*worker.Thread){func(x *worker.Thread) { x.ID = "foreign" }, func(x *worker.Thread) { x.TurnID = "old" }, func(x *worker.Thread) { x.ProcessGeneration++ }, func(x *worker.Thread) { x.BindingEpoch++ }, func(x *worker.Thread) { x.TaskID = "foreign" }, func(x *worker.Thread) { x.AttemptID = "foreign" }} {
				wrong := e.Thread
				mutate(&wrong)
				if err := p.s.Answer(p.ctx, wrong, worker.Answer{RequestID: r.ID, Result: json.RawMessage(tc.result)}); err == nil {
					t.Fatal("foreign answer accepted")
				}
			}
			if err := p.s.Answer(p.ctx, e.Thread, worker.Answer{RequestID: r.ID, Result: json.RawMessage(tc.result)}); err != nil {
				t.Fatal(err)
			}
			reply := p.receivedMethod(t, "")
			if string(reply.ID) != `9007199254740993` || string(reply.Result) != tc.result {
				t.Fatalf("reply=%+v", reply)
			}
			if snap := p.snapshot(t, th); len(snap.Pending) != 0 {
				t.Fatalf("pending after answer=%+v", snap.Pending)
			}
		})
	}
}

func TestLN133ResolvedAndTypedIDs(t *testing.T) {
	p := newAdapterPeer(t, `{"rateLimits":{}}`)
	th := p.start(t)
	p.send(requestFrame(`7`, "item/commandExecution/requestApproval", approvalParams))
	number := p.event(t, worker.ApprovalEvent)
	p.send(requestFrame(`"7"`, "item/commandExecution/requestApproval", approvalParams))
	str := p.event(t, worker.ApprovalEvent)
	if number.Approval.ID == str.Approval.ID {
		t.Fatal("string and numeric IDs collided")
	}
	p.send(frame{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"foreign","requestId":7}`)})
	if snap := p.snapshot(t, th); len(snap.Pending) != 2 {
		t.Fatal("foreign resolution changed pending")
	}
	p.send(frame{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"thread","requestId":7}`)})
	if snap := p.snapshot(t, th); len(snap.Pending) != 1 || snap.Pending[0].ID != str.Approval.ID {
		t.Fatalf("resolution=%+v", snap.Pending)
	}
	if err := p.s.Answer(p.ctx, number.Thread, worker.Answer{RequestID: number.Approval.ID, Result: json.RawMessage(`{"decision":"accept"}`)}); err == nil {
		t.Fatal("resolved answer accepted")
	}
	if err := p.s.Answer(p.ctx, str.Thread, worker.Answer{RequestID: str.Approval.ID, Result: json.RawMessage(`{"decision":"decline"}`)}); err != nil {
		t.Fatal(err)
	}
	if reply := p.receivedMethod(t, ""); string(reply.ID) != `"7"` {
		t.Fatalf("ID=%s", reply.ID)
	}
}

func TestLN133PendingBeforeTurnReplyAndStaleTurn(t *testing.T) {
	early := requestFrame(`1`, "item/commandExecution/requestApproval", approvalParams)
	p := newAdapterPeer(t, `{"rateLimits":{}}`, early)
	th := p.start(t)
	e := p.event(t, worker.ApprovalEvent)
	if snap := p.snapshot(t, th); snap.State != worker.Waiting {
		t.Fatal("turn reply erased pending")
	}
	p.send(frame{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn-1","status":"completed"}}`)})
	snap := p.snapshot(t, th)
	if len(snap.Pending) != 0 {
		t.Fatal("finished turn retained pending")
	}
	if err := p.s.Answer(p.ctx, e.Thread, worker.Answer{RequestID: e.Approval.ID, Result: json.RawMessage(`{"decision":"accept"}`)}); err == nil {
		t.Fatal("finished turn answer accepted")
	}
}
