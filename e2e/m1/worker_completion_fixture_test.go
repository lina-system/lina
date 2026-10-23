// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestWorkerCompletionFixtureRequestLayout(t *testing.T) {
	message := func(role, content string) provider.Item {
		raw, err := json.Marshal(map[string]string{"role": role, "content": content})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	evidence := func(origin, text string) provider.Item {
		admitted, err := guard.Admit(guard.EntryTool, origin, text)
		if err != nil {
			t.Fatal(err)
		}
		view, _ := admitted.Evidence()
		return message(view.Role, view.Text)
	}
	const marker = "FIXTURE_WORKER_RESULT"
	completion := evidence("worker-session:fixture", marker)
	for _, tc := range []struct {
		name     string
		current  provider.Item
		history  []provider.Item
		followup []provider.Item
		handled  bool
		result   bool
	}{
		{name: "completion before context", current: completion, handled: true, result: true},
		{name: "completion with tool discovery", current: completion, handled: true, result: true,
			followup: []provider.Item{json.RawMessage(`{"type":"function_call_output","call_id":"discovery","output":"{}"}`)}},
		{name: "person after prior completion", current: message("user", "Continue the worker."), history: []provider.Item{completion}},
		{name: "external input after prior completion", current: evidence("channel:fixture", "A new message."), history: []provider.Item{completion}},
		{name: "result only in context", current: evidence("worker-session:fixture", "No output."), handled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, report := assemble.Assemble(assemble.Input{
				Core: "Fixture rules", AgentID: "fixture", PersonaRevision: "v1", Persona: agents.Layers{Core: "Fixture persona"},
				Model: "fixture", MaxTokens: 1000, History: tc.history, Current: tc.current,
				Packet: assemble.ContextPacket{
					{ID: "clock:fixture", Kind: assemble.Clock, Text: "2026-10-23T00:00:00Z"},
					{ID: "name:fixture", Kind: assemble.Commitments, Origin: "name-memory:fixture", Text: "Call the person Sion."},
					{ID: "memory:fixture", Kind: assemble.Memory, Text: marker},
				},
			}, func(r provider.Request) int { return len(r.Items) })
			if report.Failure != "" {
				t.Fatal(report.Failure)
			}
			request.Items = append(request.Items, tc.followup...)
			w := httptest.NewRecorder()
			if got := respondWorkerCompletion(w, request.Items, marker); got != tc.handled {
				t.Fatalf("completion handled=%t, want %t", got, tc.handled)
			}
			if got := strings.Contains(w.Body.String(), marker); got != tc.result {
				t.Fatalf("worker result returned=%t, want %t: %s", got, tc.result, w.Body.String())
			}
		})
	}
}
