// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestWorkerCompletionFixtureInput(t *testing.T) {
	message := func(role, content string) provider.Item {
		raw, err := json.Marshal(map[string]string{"role": role, "content": content})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	evidence := func(origin, text string) provider.Item {
		m, err := guard.Admit(guard.EntryTool, origin, text)
		if err != nil {
			t.Fatal(err)
		}
		e, _ := m.Evidence()
		return message(e.Role, e.Text)
	}
	const result = "CURRENT_WORKER_RESULT"
	completion := evidence("worker-session:current", result)
	for _, tc := range []struct {
		name    string
		current provider.Item
		context bool
		tools   bool
		want    bool
	}{
		{"completion", completion, false, false, true},
		{"completion-with-context", completion, true, false, true},
		{"completion-after-discovery", completion, true, true, true},
		{"person-after-completion", message("user", "Start another task."), true, false, false},
		{"other-evidence-after-completion", evidence("plugin:result", "Another result."), true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := assemble.Input{
				AgentID: "fixture-agent", PersonaRevision: "fixture-revision", Model: "fixture-model", MaxTokens: 100,
				History: []provider.Item{evidence("worker-session:previous", "PREVIOUS_WORKER_RESULT")}, Current: tc.current,
			}
			if tc.context {
				input.Packet = assemble.ContextPacket{
					{ID: "clock:fixture-turn", Kind: assemble.Clock, Text: "2026-10-23T00:00:00Z"},
					{ID: "preferences", Kind: assemble.Commitments, Origin: "conversation-questions:fixture", Text: "Use concise replies."},
					{ID: "settings", Kind: assemble.Commitments, Origin: "persona-record:fixture", Text: "Prepared settings."},
					{ID: "retrieved", Kind: assemble.External, Origin: "worker-session:context", Text: "RETRIEVED_WORKER_RESULT"},
				}
			}
			request, report := assemble.Assemble(input, func(provider.Request) int { return 1 })
			if report.Failure != "" {
				t.Fatal(report.Failure)
			}
			if tc.tools {
				request.Items = append(request.Items,
					json.RawMessage(`{"type":"function_call","call_id":"discover","name":"load_tools","arguments":"{}"}`),
					json.RawMessage(`{"type":"function_call_output","call_id":"discover","output":"{}"}`))
			}
			inputs := make([]json.RawMessage, len(request.Items))
			for i, raw := range request.Items {
				inputs[i] = json.RawMessage(raw)
			}
			w := httptest.NewRecorder()
			if got := respondWorkerCompletion(w, inputs, result); got != tc.want {
				t.Fatalf("handled=%t, want %t; response=%s", got, tc.want, w.Body.String())
			}
			if tc.want && !strings.Contains(w.Body.String(), result) {
				t.Fatalf("current worker result missing: %s", w.Body.String())
			}
			if !tc.want && w.Body.Len() != 0 {
				t.Fatalf("unrelated input received a worker response: %s", w.Body.String())
			}
		})
	}
}
