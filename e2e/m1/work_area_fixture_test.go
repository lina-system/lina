// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestWorkAreaCompletionRequest(t *testing.T) {
	project := func(entry guard.Entry, body string) provider.Item {
		t.Helper()
		message, err := guard.Admit(entry, "fixture", body)
		if err != nil {
			t.Fatal(err)
		}
		masked, err := secrets.Mask(message.Body())
		if err != nil {
			t.Fatal(err)
		}
		items, err := assemble.History([]ledger.Event{{
			ConversationID: "conversation", AgentID: "agent", SpeakerKind: message.SpeakerKind(),
			Origin: ledger.Origin{Kind: "native", Entry: entry, Locator: "fixture"}, Body: masked,
		}}, "conversation", "agent")
		if err != nil || len(items) != 1 {
			t.Fatalf("project input: %v, items=%d", err, len(items))
		}
		return items[0]
	}
	completion := project(guard.EntrySystem, "Completion fixture\n"+`{"kind":"work_completion"}`)
	owner := project(guard.EntryOwner, "Preserve the completed result.")
	packet := assemble.ContextPacket{
		{ID: "clock:fixture", Kind: assemble.Clock, Text: "fixture time"},
		{ID: "memory", Kind: assemble.Memory, Text: "fixture memory"},
		{ID: "material", Kind: assemble.Material, Revision: "1", Text: "fixture material"},
		{ID: "external", Kind: assemble.External, Origin: "fixture", Text: string(completion)},
	}
	for _, tc := range []struct {
		name    string
		history []provider.Item
		current provider.Item
		packet  assemble.ContextPacket
		want    bool
	}{
		{"completion-only", nil, completion, nil, true},
		{"completion-before-context", nil, completion, packet, true},
		{"owner-after-completion", []provider.Item{completion}, owner, packet, false},
		{"completion-after-owner", []provider.Item{owner}, completion, packet, true},
		{"completion-in-evidence-only", nil, owner, packet, false},
		{"external-input-after-completion", []provider.Item{completion}, project(guard.EntryTool, "A different worker result."), packet, false},
		{"other-system-input", []provider.Item{completion}, project(guard.EntrySystem, "Notice\n"+`{"kind":"other"}`), packet, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, report := assemble.Assemble(assemble.Input{
				Core: "fixture", AgentID: "agent", PersonaRevision: "revision", Model: "fixture", MaxTokens: 100,
				History: tc.history, Current: tc.current, Packet: tc.packet,
			}, func(r provider.Request) int { return len(r.Items) })
			if report.Failure != "" {
				t.Fatal(report.Failure)
			}
			input := make([]json.RawMessage, len(request.Items))
			copy(input, request.Items)
			response := httptest.NewRecorder()
			if got := scriptedWorkCompletion(response, input); got != tc.want {
				t.Fatalf("completion handled=%v, want %v", got, tc.want)
			}
			if wrote := response.Body.Len() > 0; wrote != tc.want {
				t.Fatalf("completion response written=%v, want %v", wrote, tc.want)
			}
		})
	}
}
