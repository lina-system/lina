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

// Exercise the model fixture against Core's real request layout. Context can
// contain user-role evidence, including an older worker result, after the input.
func TestCodexCompletionContext(t *testing.T) {
	t.Parallel()
	message := func(role, content string) provider.Item {
		return mustJSON(t, map[string]string{"role": role, "content": content})
	}
	evidence := func(origin, body string) provider.Item {
		m, err := guard.Admit(guard.EntryTool, origin, body)
		if err != nil {
			t.Fatal(err)
		}
		e, _ := m.Evidence()
		return message(e.Role, e.Text)
	}
	const result = "CURRENT_WORKER_RESULT"
	completion := evidence("worker-session:current", result)
	prior := evidence("worker-session:prior", "PRIOR_WORKER_RESULT")
	for _, tc := range []struct {
		name    string
		current provider.Item
		packet  assemble.ContextPacket
		want    bool
	}{
		{"clock only", completion, nil, true},
		{"persona settings", completion, assemble.ContextPacket{{ID: "persona-settings", Kind: assemble.Commitments, Origin: "persona-record:agent", Text: `{"kind":"persona_settings","values":{}}`}}, true},
		{"recalled context", completion, assemble.ContextPacket{{ID: "memory", Kind: assemble.Memory, Text: "Remembered preference"}}, true},
		{"other worker context", completion, assemble.ContextPacket{{ID: "worker", Kind: assemble.External, Origin: "worker-session:prior", Text: "PRIOR_WORKER_RESULT"}}, true},
		{"new person input", message("user", "Continue with another task."), nil, false},
		{"person input with worker context", message("user", "Continue with another task."), assemble.ContextPacket{{ID: "worker", Kind: assemble.External, Origin: "worker-session:prior", Text: "PRIOR_WORKER_RESULT"}}, false},
		{"other event", evidence("question-answer:current", "Recorded answer"), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, report := assemble.Assemble(assemble.Input{
				AgentID: "agent", PersonaRevision: "revision", Model: "fixture", MaxTokens: 100,
				History: []provider.Item{prior, message("assistant", "Prior result recorded.")},
				Current: tc.current,
				Packet:  append(assemble.ContextPacket{{ID: "clock:turn", Kind: assemble.Clock, Text: "2026-10-23T00:00:00Z"}}, tc.packet...),
			}, func(r provider.Request) int { return len(r.Items) })
			if report.Failure != "" {
				t.Fatal(report)
			}
			for _, discovery := range []bool{false, true} {
				inputs := append([]json.RawMessage(nil), req.Items...)
				if discovery {
					inputs = append(inputs, json.RawMessage(`{"type":"function_call","name":"load_tools","call_id":"discovery","arguments":"{}"}`), json.RawMessage(`{"type":"function_call_output","call_id":"discovery","output":"{}"}`))
				}
				w := httptest.NewRecorder()
				if got := respondWorkerCompletion(w, inputs, result); got != tc.want {
					t.Errorf("completion recognized=%t, want %t (discovery=%t)", got, tc.want, discovery)
				}
				if tc.want && !strings.Contains(w.Body.String(), result) {
					t.Errorf("current worker result missing (discovery=%t): %s", discovery, w.Body.String())
				}
				if !tc.want && w.Body.Len() != 0 {
					t.Errorf("non-completion request received a completion reply: %s", w.Body.String())
				}
			}
		})
	}
}
