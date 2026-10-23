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

// Exercise the scripted provider with Core's actual input/context layout.
// A completion in history or retrieved context must not answer a new input.
func TestCodexWorkCompletionFixtureContext(t *testing.T) {
	message := func(role, content string) json.RawMessage {
		return mustJSON(t, map[string]string{"role": role, "content": content})
	}
	const result = "WORK_COMPLETION_ARTIFACT"
	completion, err := guard.Admit(guard.EntryTool, "worker-session:fixture", result)
	if err != nil {
		t.Fatal(err)
	}
	evidence, _ := completion.Evidence()
	workerInput := message(evidence.Role, evidence.Text)
	personInput := message("user", "Check the next task.")
	for _, tc := range []struct {
		name          string
		current       provider.Item
		history       []provider.Item
		discovery     bool
		workerContext bool
		want          bool
	}{
		{name: "completion", current: workerInput, want: true},
		{name: "completion_after_discovery", current: workerInput, discovery: true, want: true},
		{name: "completion_with_older_context", current: workerInput, workerContext: true, want: true},
		{name: "person_after_completion", current: personInput, history: []provider.Item{workerInput}},
		{name: "person_with_older_context", current: personInput, history: []provider.Item{workerInput}, workerContext: true},
		{name: "person_after_completion_and_discovery", current: personInput, history: []provider.Item{workerInput}, discovery: true, workerContext: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packet := assemble.ContextPacket{
				{ID: "clock:turn:fixture", Kind: assemble.Clock, Text: "2026-10-23T00:00:00Z"},
				{ID: "memory", Kind: assemble.Memory, Text: "An unrelated remembered fact."},
			}
			if tc.workerContext {
				packet = append(packet, assemble.ContextItem{ID: "worker-context", Kind: assemble.External, Origin: "worker-session:older", Text: "OLDER_WORKER_ARTIFACT"})
			}
			req, report := assemble.Assemble(assemble.Input{
				AgentID: "fixture", PersonaRevision: "v1", Model: "fixture-model", MaxTokens: 100,
				Current: tc.current, History: tc.history, Packet: packet,
			}, func(provider.Request) int { return 1 })
			if report.Failure != "" {
				t.Fatal(report.Failure)
			}
			if tc.discovery {
				req.Items = append(req.Items,
					mustJSON(t, map[string]string{"type": "function_call", "name": "load_tools", "call_id": "discover", "arguments": `{}`}),
					mustJSON(t, map[string]string{"type": "function_call_output", "call_id": "discover", "output": `{}`}))
			}
			response := httptest.NewRecorder()
			if got := respondWorkerCompletion(response, req.Items, result); got != tc.want {
				t.Fatalf("completion recognized=%t, want %t", got, tc.want)
			}
			if tc.want && !strings.Contains(response.Body.String(), result) {
				t.Fatal("response lost the current worker artifact")
			}
		})
	}
}
