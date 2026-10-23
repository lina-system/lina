// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestWorkerCompletionFixtureContext(t *testing.T) {
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
	const result = "WORKER_FIXTURE_RESULT"
	completed := evidence("worker-session:fixture", result)
	interrupted := evidence("worker-session:fixture", `{"status":"interrupted"}`)
	for _, tc := range []struct {
		name    string
		current provider.Item
		handled bool
	}{
		{name: "interrupted", current: interrupted, handled: true},
		{name: "other external input", current: evidence("plugin:fixture", "New event")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Use the real assembler: both the current input and retrieved
			// ContextPacket evidence have the user role. Older results stay
			// in history and cannot identify the turn being answered.
			req, report := assemble.Assemble(assemble.Input{
				AgentID: "fixture", PersonaRevision: "v1", Model: "fixture", MaxTokens: 10000,
				History: []provider.Item{completed, message("assistant", "Previous response")},
				Current: tc.current,
				Packet: assemble.ContextPacket{
					{ID: "clock:fixture", Kind: assemble.Clock, Text: "2026-10-23T00:00:00Z"},
					{ID: "persona-settings:fixture", Kind: assemble.Commitments, Origin: "persona-record:fixture", Text: "{}"},
					{ID: "memory:fixture", Kind: assemble.Memory, Text: result},
				},
			}, func(provider.Request) int { return 1 })
			if report.Failure != "" {
				t.Fatal(report.Failure)
			}
			w := httptest.NewRecorder()
			if got := respondWorkerCompletion(w, req.Items, result); got != tc.handled {
				t.Fatalf("handled=%t, want %t", got, tc.handled)
			}
			if strings.Contains(w.Body.String(), result) {
				t.Fatal("reported an older worker result as the current outcome")
			}
			if !tc.handled && w.Body.Len() != 0 {
				t.Fatal("non-worker input received a worker response")
			}
		})
	}
}
