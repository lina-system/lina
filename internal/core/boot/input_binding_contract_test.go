// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/intakereplay"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func bindingCallStep(id, args string) turnStep {
	call := provider.ToolCall{ID: id, Name: "bind_input", Arguments: args}
	raw, _ := json.Marshal(map[string]string{"type": "function_call", "call_id": id, "name": call.Name, "arguments": args})
	return turnStep{events: []provider.Event{{Kind: provider.ItemDone, Item: raw}, {Kind: provider.ToolCallReady, Call: &call}, {Kind: provider.Terminal}}}
}

func bindingToolResult(t *testing.T, req provider.Request, id string) loop.Result {
	t.Helper()
	for _, raw := range req.Items {
		var item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		}
		_ = json.Unmarshal(raw, &item)
		if item.Type == "function_call_output" && item.CallID == id {
			var result loop.Result
			if err := json.Unmarshal([]byte(item.Output), &result); err != nil {
				t.Fatal(err)
			}
			return result
		}
	}
	t.Fatalf("missing result for %s", id)
	return loop.Result{}
}

func TestModelBindingContractTextInput(t *testing.T) {
	r, p := turnRuntime(t)
	c := intakereplay.Case{Version: 1, ID: "synthetic-text-input", Set: "hard", State: &binding.State{Clock: time.Now().UTC(), Focus: binding.Target{Class: "none"}, Questions: []binding.Question{}, Surfaces: map[string]string{}, Items: []binding.Item{}}, Input: binding.Input{Text: "Save a garden task."}, Gold: []intakereplay.Gold{{Target: binding.Target{Class: "new", Kind: "todo"}}}}
	p.steps = []turnStep{bindingCallStep("text-input", `{"input":"Save a garden task.","bindings":[{"request":0,"quote":"Save a garden task.","target":{"class":"new","kind":"todo"}}]}`), {events: turnText("Bound."), before: func(req provider.Request) {
		if result := bindingToolResult(t, req, "text-input"); result.Code != loop.OK {
			t.Fatalf("message text was not treated as current input: %+v", result)
		}
	}}}
	got, err := ReplayIntakeModelCase(t.Context(), c, r.route)
	if err != nil || len(got) != 1 || got[0].Decider != "model" || got[0].Target != (binding.Target{Class: "new", Kind: "todo"}) {
		t.Fatalf("model binding: %+v %v", got, err)
	}
}

func TestModelBindingContractNewTargetRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, target, field, reason string
		allowed                     []string
	}{
		{"kind", `{"class":"new","kind":"note"}`, "target.kind", "unsupported_kind", []string{"initiative", "project", "todo", "task", "run"}},
		{"parent", `{"class":"new","kind":"todo","parent":"active-task"}`, "target.parent", "invalid_parent", []string{"garden"}},
		{"id", `{"class":"new","kind":"todo","id":"invented"}`, "target.id", "must_be_empty", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, p := turnRuntime(t)
			now := time.Now().UTC()
			c := intakereplay.Case{Version: 1, ID: "synthetic-new-target", Set: "hard", State: &binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Questions: []binding.Question{}, Surfaces: map[string]string{}, Items: []binding.Item{{ID: "garden", Kind: "project", Title: "Garden", State: "active", LastActivity: now}, {ID: "active-task", Kind: "task", Title: "Planting", State: "running", LastActivity: now}, {ID: "closed-project", Kind: "project", Title: "Finished", State: "completed", LastActivity: now}}}, Input: binding.Input{Text: "Save a garden task."}, Gold: []intakereplay.Gold{{Target: binding.Target{Class: "new", Kind: "todo"}}}}
			fix := bindingCallStep("corrected", `{"bindings":[{"request":0,"quote":"Save a garden task.","target":{"class":"new","kind":"todo","parent":"garden"}}]}`)
			fix.before = func(req provider.Request) {
				result := bindingToolResult(t, req, "invalid")
				// Inspect structured recovery fields, never error prose.
				var detail struct {
					Field              string   `json:"field"`
					Reason             string   `json:"reason"`
					Allowed            []string `json:"allowed"`
					AllowedParentKinds []string `json:"allowed_parent_kinds"`
				}
				_ = json.Unmarshal(result.Output, &detail)
				if result.Code != loop.Invalid || detail.Field != tc.field || detail.Reason != tc.reason || !slices.Equal(detail.Allowed, tc.allowed) {
					t.Fatalf("uncorrectable new target error: code=%s detail=%+v", result.Code, detail)
				}
				if tc.name == "parent" && !slices.Equal(detail.AllowedParentKinds, []string{"initiative", "project"}) {
					t.Fatalf("missing parent kinds: %+v", detail)
				}
			}
			p.steps = []turnStep{bindingCallStep("invalid", `{"bindings":[{"request":0,"quote":"Save a garden task.","target":`+tc.target+`}]}`), fix, {events: turnText("Bound.")}}
			got, err := ReplayIntakeModelCase(t.Context(), c, r.route)
			if err != nil || len(got) != 1 || got[0].Decider != "model" || got[0].Target.Parent != "garden" {
				t.Fatalf("recovery: %+v %v", got, err)
			}
			if len(p.requests) != 3 {
				t.Fatalf("recovery took %d requests", len(p.requests))
			}
		})
	}
}

func TestModelBindingContractWorkInput(t *testing.T) {
	r, _ := turnRuntime(t)
	input := questionPersonInput(t, r, "contract-work", "Save a garden task.")
	tools := modelBindingTools(t, r, input)
	result := questionCall(t, tools, "write_record", "create", map[string]any{"kind": "todo", "title": "Garden", "note": "Plant seeds.", "input_binding": map[string]any{"input": input.Body.Text(), "request": 0}})
	if result.Code != loop.OK {
		t.Fatalf("work input text rejected: %+v", result)
	}
}

func TestModelBindingContractEventSelection(t *testing.T) {
	r, _ := turnRuntime(t)
	current := questionPersonInput(t, r, "current-event", "Current request.")
	consumed := questionPersonInput(t, r, "consumed-event", "Later request.")
	foreign := questionPersonInput(t, r, "unconsumed-event", "Not consumed.")
	s := &turnState{runtime: r, input: current, turnID: "contract-select", bindings: map[string]loop.Tool{}}
	// An admitted event alone is insufficient: it must belong to the live turn.
	for _, tc := range []struct {
		selector string
		want     ledger.Event
		ok       bool
	}{
		{"", current, true}, {current.ID, current, true}, {current.Body.Text(), current, true}, {consumed.ID, ledger.Event{}, false}, {foreign.ID, ledger.Event{}, false},
	} {
		got, _, err := s.bindingInput(tc.selector)
		if (err == nil) != tc.ok || got.ID != tc.want.ID {
			t.Fatalf("selector %q: id=%q err=%v", tc.selector, got.ID, err)
		}
	}
	tools := modelBindingTools(t, r, current)
	args := map[string]any{"input_event_id": current.ID, "bindings": []binding.Choice{{Quote: current.Body.Text(), Target: binding.Target{Class: "none"}}}}
	if result := questionCall(t, tools, "bind_input", "event-id", args); result.Code != loop.OK {
		t.Fatal(result)
	}
	args["input"] = foreign.ID
	if result := questionCall(t, tools, "bind_input", "foreign", args); result.Code != loop.Invalid {
		t.Fatal(result)
	}
	result := questionCall(t, tools, "write_record", "event-write", map[string]any{"kind": "todo", "title": "Selected request", "note": "Current event.", "input_binding": map[string]any{"input_event_id": current.ID, "request": 0}})
	if result.Code != loop.OK {
		t.Fatal(result)
	}
}
