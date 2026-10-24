// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/kit/acp"
)

func TestTextToolCallHeldUntilTerminal(t *testing.T) {
	a := New(Config{})
	a.life = t.Context()
	th := worker.Thread{ID: "thread", TurnID: "turn"}
	for _, chunk := range []string{"[tool", `_use id=x name=_check_inbox] {}`} {
		raw, _ := json.Marshal(map[string]any{"sessionId": "thread", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": chunk}}})
		var u wire.Update
		if err := json.Unmarshal(raw, &u.Notification); err != nil {
			t.Fatal(err)
		}
		if !a.update(th, u) {
			t.Fatal("update refused")
		}
	}
	for len(a.events) > 0 {
		if e := <-a.events; e.Progress != nil && e.Progress.Text != "" {
			t.Fatalf("text leaked before terminal: %q", e.Progress.Text)
		}
	}
	if a.Result().Output != "" {
		t.Fatal("unclassified output exposed")
	}
}

func TestTerminalTextToolCalls(t *testing.T) {
	for _, tc := range []struct {
		name, text, stop string
		real             bool
		want             string
	}{
		{"allowed", `[tool_use id=x name=_check_inbox] {}`, "end_turn", false, "dispatch"},
		{"internal", `[tool_use id=x name=_bash] {"command":"pwd"}`, "end_turn", false, "restart"},
		{"provider-stop", `[tool_use id=x name=_bash] {"command":"pwd"}`, "stop", false, "restart"},
		{"incomplete", `[tool_use id=x name=coordinate] {"request":`, "end_turn", false, "restart"},
		{"real-tool", `[tool_use id=x name=_bash] {}`, "end_turn", true, "none"},
		{"quoted", "```\n[tool_use id=x name=_bash] {}\n```", "end_turn", false, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ctx, l, spec := fixture(t)
			spec.TaskPacket = tc.stop
			updates := []any{}
			if tc.real {
				updates = append(updates, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "real", "title": "Command", "status": "completed"})
			}
			for _, part := range []string{tc.text[:5], tc.text[5:]} {
				updates = append(updates, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": part}})
			}
			raw, _ := json.Marshal(updates)
			a.cfg.Environment = append(a.cfg.Environment, "ACP_UPDATES="+string(raw))
			th, err := a.Start(ctx, l, spec)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			for {
				e := event(t, ctx, a)
				if e.Progress != nil && tc.want != "none" && strings.Contains(e.Progress.Text, "[tool") {
					t.Fatal("text leaked", e.Progress.Text)
				}
				if e.ToolCall != nil {
					calls++
					if tc.want != "dispatch" || e.ToolCall.Name != "check_inbox" {
						t.Fatalf("unexpected call: %+v", e.ToolCall)
					}
					if snap, err := a.Snapshot(ctx, th); err != nil || snap.Finish != nil || snap.Output != "" {
						t.Fatal("early completion", snap, err)
					}
					if err := a.Answer(ctx, th, worker.Answer{RequestID: e.ToolCall.ID, Result: json.RawMessage(`{"success":true,"contentItems":[]}`)}); err != nil {
						t.Fatal(err)
					}
				}
				if e.Finish != nil {
					wantCode := ""
					if tc.want == "restart" {
						wantCode = worker.TextCallFailure
					}
					if tc.want == "dispatch" {
						wantCode = "text_tool_call_recovered"
					}
					if e.Finish.Code != wantCode {
						t.Fatal(e.Finish)
					}
					break
				}
			}
			if (calls == 1) != (tc.want == "dispatch") {
				t.Fatal("dispatch count", calls)
			}
			snap, err := a.Snapshot(ctx, th)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "restart" && !snap.EffectsKnown {
				t.Fatal("effect-free text turn cannot recover", snap)
			}
			if tc.want != "none" && (snap.Output != "" || a.Result().Output != "") {
				t.Fatal("raw completion leaked")
			}
		})
	}
}
