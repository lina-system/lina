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

// Use Core's request assembler so fixture routing follows the same current
// input / ContextPacket boundary as the installed worker journeys.
func TestWorkerCompletionFixtureCurrentInput(t *testing.T) {
	const result = "fixture-worker-artifact"
	message := func(role, content string) provider.Item {
		raw, err := json.Marshal(map[string]string{"role": role, "content": content})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	worker, err := guard.Admit(guard.EntryTool, "worker-session:fixture", result)
	if err != nil {
		t.Fatal(err)
	}
	evidence, _ := worker.Evidence()
	completion := message(evidence.Role, evidence.Text)
	for _, tc := range []struct {
		name    string
		current provider.Item
		history []provider.Item
		want    bool
	}{
		{"completion", completion, nil, true},
		{"later person input", message("user", "Inspect another file."), []provider.Item{completion}, false},
		{"later system input", message("user", "A scheduled check is due."), []provider.Item{completion}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, report := assemble.Assemble(assemble.Input{
				AgentID: "fixture", PersonaRevision: "fixture", Model: "fixture", MaxTokens: 1000,
				History: tc.history, Current: tc.current,
				Packet: assemble.ContextPacket{
					{ID: "clock:fixture", Kind: assemble.Clock, Text: "2026-10-23T00:00:00Z"},
					{ID: "persona-settings:fixture", Kind: assemble.Commitments, Origin: "persona-record:fixture", Text: "Synthetic persona settings"},
					{ID: "memory:fixture", Kind: assemble.Memory, Origin: "memory:fixture", Text: "Synthetic recalled evidence"},
				},
			}, func(provider.Request) int { return 1 })
			if report.Failure != "" {
				t.Fatal(report.Failure)
			}
			// Tool discovery can append protocol items after the packet.
			request.Items = append(request.Items, provider.Item(`{"type":"function_call_output","call_id":"load-tools","output":"ok"}`))
			inputs := make([]json.RawMessage, len(request.Items))
			for i, item := range request.Items {
				inputs[i] = json.RawMessage(item)
			}
			response := httptest.NewRecorder()
			if got := respondWorkerCompletion(response, inputs, result); got != tc.want {
				t.Fatalf("completion handled=%t, want %t", got, tc.want)
			}
			if tc.want && !strings.Contains(response.Body.String(), result) {
				t.Fatal("completion response lost the worker result")
			}
			if !tc.want && response.Body.Len() != 0 {
				t.Fatal("previous completion consumed a later input")
			}
		})
	}
}
