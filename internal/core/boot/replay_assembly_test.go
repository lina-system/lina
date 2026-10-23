// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestReplayRequestLayoutBaselines(t *testing.T) {
	// These exact item bytes are independent baselines for both recorded layouts.
	catalog := provider.Item(`{"role":"developer","content":"null"}`)
	profile := provider.Item(`{"content":"Remembered statement (preference): Use short replies.","role":"user"}`)
	history := provider.Item(`{ "type":"reasoning", "encrypted_content":"opaque-history" }`)
	current := provider.Item(`{"role":"user","content":"Continue."}`)
	clock := provider.Item(`{"role":"developer","content":"2026-01-02T03:04:05Z\nReferences: [{\"id\":\"clock:turn\"}]"}`)
	for _, tc := range []struct {
		name, layout string
		want         []provider.Item
	}{
		{"legacy", "", []provider.Item{catalog, profile, history, clock, current}},
		{"current", ledger.InputBeforeContextV1, []provider.Item{catalog, history, current, clock, profile}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep := replay.Episode{TurnID: "turn", Conversation: "conversation", Persona: agents.Layers{Core: "persona"}, Items: []provider.Item{history, current}, Evidence: ledger.RequestEvidence{Model: "fixture-model", Assembly: ledger.Assembly{AgentID: "agent", AgentName: "Agent", OwnerPersonID: "owner", PersonaRevision: "revision", InstructionFormat: "persona-v1", RequestLayout: tc.layout}}}
			before, _ := json.Marshal(ep)
			items, err := rebuildPersonaItems(ep, []replay.Context{{Source: ledger.ContextSource{Reference: ledger.Reference{ID: "clock:turn"}, Source: "clock-v1"}, Text: "2026-01-02T03:04:05Z"}}, []provider.Item{profile})
			if err != nil || !reflect.DeepEqual(items, tc.want) {
				t.Fatalf("layout %q: items=%s err=%v, want=%s", tc.layout, items, err, tc.want)
			}
			after, _ := json.Marshal(ep)
			if string(before) != string(after) {
				t.Fatal("rebuild mutated recorded input")
			}
			ep.Items = items
			request, err := replay.Request(ep, nil, "", "", replayInstructions)
			if err != nil {
				t.Fatal(err)
			}
			request.Purpose = provider.Turn
			original := request
			original.Items = tc.want
			if assemble.Digest(request) != assemble.Digest(original) {
				t.Fatal("recorded request digest changed")
			}
		})
	}
	ep := replay.Episode{Items: []provider.Item{current}, Evidence: ledger.RequestEvidence{Assembly: ledger.Assembly{RequestLayout: "future-layout"}}}
	if _, err := rebuildPersonaItems(ep, nil, nil); err != replay.Exclusion("missing_assembly") {
		t.Fatalf("unknown layout reconstructed: %v", err)
	}
	if _, err := replay.Request(ep, nil, "", "", replayInstructions); err != replay.Exclusion("missing_assembly") {
		t.Fatalf("unknown layout replayed: %v", err)
	}
}

func TestReplayKeepsInstructionToolFormat(t *testing.T) {
	tools := []provider.ToolDecl{[]byte(`{"name":"remote_agent","parameters":{"type":"object"}}`)}
	method := `{"schema":1,"preset":"lina"}`
	for _, version := range []string{"persona-tui-work-method-v1", "persona-tui-work-method-v2"} {
		t.Run(version, func(t *testing.T) {
			ep := replay.Episode{WorkMethod: method, Persona: agents.Layers{Core: "fixture"}, Evidence: ledger.RequestEvidence{Tools: tools, Assembly: ledger.Assembly{AgentID: "agent", AgentName: "Agent", OwnerPersonID: "owner", InstructionFormat: version}}}
			request, err := replay.Request(ep, nil, "", "", replayInstructions)
			if err != nil {
				t.Fatal(err)
			}
			_, block, ok := strings.Cut(request.Instructions, "<enabled_tools>\n")
			if !ok {
				t.Fatal("missing enabled tools")
			}
			block, _, _ = strings.Cut(block, "\n</enabled_tools>")
			if version == "persona-tui-work-method-v1" {
				var declarations []provider.ToolDecl
				if err := json.Unmarshal([]byte(block), &declarations); err != nil || !reflect.DeepEqual(declarations, tools) {
					t.Fatalf("legacy schema evidence changed: %s %v", block, err)
				}
			} else {
				var names []string
				if err := json.Unmarshal([]byte(block), &names); err != nil || !reflect.DeepEqual(names, []string{"remote_agent"}) {
					t.Fatalf("new names evidence changed: %s %v", block, err)
				}
			}
			_, recorded, ok := strings.Cut(request.Instructions, "<work_method>\n")
			recorded, _, _ = strings.Cut(recorded, "\n</work_method>")
			var gotMethod, wantMethod map[string]any
			if !ok || json.Unmarshal([]byte(recorded), &gotMethod) != nil || json.Unmarshal([]byte(method), &wantMethod) != nil || !reflect.DeepEqual(gotMethod, wantMethod) {
				t.Fatal("pinned work method lost")
			}
		})
	}
}
