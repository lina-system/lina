// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestTextToolCallHeldUntilTerminal(t *testing.T) {
	for _, text := range []string{`[tool_use id=x name=_check_inbox] {}`, `[tool_use id=x name=_bash] {"command":"pwd"}`} {
		t.Run(text, func(t *testing.T) {
			s := &Session{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn"}, State: worker.Active}, events: make(chan worker.Event, 257), finished: map[string]bool{}}
			for _, chunk := range []string{text[:5], text[5:]} {
				raw, _ := json.Marshal(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "msg", "delta": chunk}})
				if err := s.notification(raw); err != nil {
					t.Fatal(err)
				}
			}
			for len(s.events) > 0 {
				if e := <-s.events; e.Progress != nil && e.Progress.Text != "" {
					t.Fatalf("text leaked before terminal: %q", e.Progress.Text)
				}
			}
			if err := s.notification([]byte(`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed"}}}`)); err != nil {
				t.Fatal(err)
			}
			e := <-s.events
			if e.Finish == nil || e.Finish.Status != worker.Failed || e.Finish.Code != "text_tool_call" {
				t.Fatalf("undeclared text call completed: %+v", e)
			}
			if s.snapshot.Output != "" {
				t.Fatal("contaminated output", s.snapshot.Output)
			}
		})
	}
}

func TestRecoveredTextCallRequiresCoreAnswer(t *testing.T) {
	for _, name := range []string{"read_projection", "_check_inbox", "coordinate", "ask"} {
		t.Run(name, func(t *testing.T) {
			p := newAdapterPeer(t, `{"rateLimits":{}}`)
			th := p.start(t, worker.WorkToolDeclarations()...)
			args := `{}`
			if name == "coordinate" {
				args = `{"request":"Check progress"}`
			}
			if name == "ask" {
				args = `{"question":"Which file?"}`
			}
			raw, _ := json.Marshal(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": th.ID, "turn": map[string]any{"id": th.TurnID, "status": "completed", "items": []any{map[string]any{"type": "agentMessage", "id": "msg", "text": "[tool_use id=x name=" + name + "] " + args}}}}})
			if err := p.s.notification(raw); err != nil {
				t.Fatal(err)
			}
			var call worker.Event
			for len(p.s.events) > 0 {
				e := <-p.s.events
				if e.Finish != nil {
					t.Fatal("finished before dispatch")
				}
				if e.ToolCall != nil {
					call = e
				}
			}
			if call.ToolCall == nil || call.ToolCall.Name != strings.TrimPrefix(name, "_") {
				t.Fatalf("missing work tool: %+v", call)
			}
			snap, err := p.s.Reconcile(p.ctx, th)
			if err != nil || snap.Finish != nil || len(snap.Pending) != 1 || snap.Output != "" {
				t.Fatalf("premature snapshot: %+v %v", snap, err)
			}
			answer := worker.Answer{RequestID: call.ToolCall.ID, Result: json.RawMessage(`{"success":true,"contentItems":[{"type":"inputText","text":"accepted"}]}`)}
			if err = p.s.Answer(p.ctx, th, answer); err != nil {
				t.Fatal(err)
			}
			if err = p.s.Answer(p.ctx, th, answer); err == nil {
				t.Fatal("recovered call answered twice")
			}
			finishes := 0
			for len(p.s.events) > 0 {
				e := <-p.s.events
				if e.Finish != nil {
					finishes++
					if e.Finish.Code != "text_tool_call_recovered" {
						t.Fatal(e)
					}
				}
			}
			if finishes != 1 {
				t.Fatal("missing completion", finishes)
			}
			// A terminal read cannot resurrect the contaminated protocol transcript.
			snap, err = p.s.Reconcile(p.ctx, th)
			if err != nil || snap.Output != "" || snap.Finish == nil {
				t.Fatal(snap, err)
			}
		})
	}
}

func TestRealToolPreventsTextCallRecovery(t *testing.T) {
	for _, kind := range []string{"commandExecution", "webSearch", "dynamicToolCall", "futureTool"} {
		t.Run(kind, func(t *testing.T) {
			s := &Session{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "turn"}, State: worker.Active}, events: make(chan worker.Event, 257), finished: map[string]bool{}, toolDeclarations: worker.WorkToolDeclarations()}
			raw, _ := json.Marshal(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed", "items": []any{map[string]any{"type": kind, "id": "real"}, map[string]any{"type": "agentMessage", "id": "msg", "text": "[tool_use id=x name=check_inbox] {}"}}}}})
			if err := s.notification(raw); err != nil {
				t.Fatal(err)
			}
			finishes := 0
			for len(s.events) > 0 {
				e := <-s.events
				if e.ToolCall != nil {
					t.Fatal("replayed after real activity")
				}
				if e.Finish != nil {
					finishes++
					if e.Finish.Status != worker.Done {
						t.Fatal(e)
					}
				}
			}
			if finishes != 1 {
				t.Fatal("missing finish")
			}
		})
	}
}

func TestReconciledTextCallNeverBecomesOutput(t *testing.T) {
	s, ctx, l := openFixture(t, "recover-text-call")
	th, err := s.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: t.TempDir(), TaskPacket: "Check result"})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Reconcile(ctx, th)
	if err != nil || snap.Output != "" || snap.Finish == nil || snap.Finish.Code != worker.TextCallFailure || !snap.EffectsKnown {
		t.Fatalf("unclassified terminal read: %+v %v", snap, err)
	}
}
