// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"fmt"
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
		name, target string
		want         binding.Target
	}{
		{"kind", `{"class":"new","kind":"note"}`, binding.Target{Class: "new", Kind: "todo"}},
		{"parent", `{"class":"new","kind":"todo","parent":"active-task"}`, binding.Target{Class: "new", Kind: "todo"}},
		{"id", `{"class":"new","kind":"todo","id":"invented"}`, binding.Target{Class: "new", Kind: "todo"}},
		{"named", `{"class":"new","kind":"todo","title":"Garden"}`, binding.Target{Class: "existing", ID: "garden", Kind: "project"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, p := turnRuntime(t)
			now := time.Now().UTC()
			c := intakereplay.Case{Version: 1, ID: "synthetic-new-target", Set: "hard", State: &binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Questions: []binding.Question{}, Surfaces: map[string]string{}, Items: []binding.Item{{ID: "garden", Kind: "project", Title: "Garden", State: "active", LastActivity: now}, {ID: "active-task", Kind: "task", Title: "Planting", State: "running", LastActivity: now}, {ID: "closed-project", Kind: "project", Title: "Finished", State: "completed", LastActivity: now}}}, Input: binding.Input{Text: "Save a garden task."}, Gold: []intakereplay.Gold{{Target: binding.Target{Class: "new", Kind: "todo"}}}}
			p.steps = []turnStep{bindingCallStep("tolerant", `{"bindings":[{"quote":"Save a garden task.","target":`+tc.target+`}]}`), {events: turnText("Bound.")}}
			got, err := ReplayIntakeModelCase(t.Context(), c, r.route)
			if err != nil || len(got) != 1 || got[0].Decider != "model" || got[0].Target != tc.want {
				t.Fatalf("new target: %+v %v", got, err)
			}
			if len(p.requests) != 2 {
				t.Fatalf("tolerable target cost %d requests", len(p.requests))
			}
		})
	}
}

// A target that names no open item, or names two, is the one argument Core
// cannot guess. It returns the candidates so the model can correct the call.
func TestModelBindingContractAmbiguousTargetCorrection(t *testing.T) {
	r, p := turnRuntime(t)
	now := time.Now().UTC()
	c := intakereplay.Case{Version: 1, ID: "synthetic-ambiguous", Set: "hard", State: &binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Questions: []binding.Question{}, Surfaces: map[string]string{}, Items: []binding.Item{{ID: "garden", Kind: "project", Title: "Garden", State: "active", LastActivity: now}}}, Input: binding.Input{Text: "Plant the beds."}, Gold: []intakereplay.Gold{{Target: binding.Target{Class: "existing", ID: "garden", Kind: "project"}}}}
	fix := bindingCallStep("corrected", `{"bindings":[{"target":"garden"}]}`)
	fix.before = func(req provider.Request) {
		result := bindingToolResult(t, req, "invented")
		var detail struct {
			Field   string   `json:"field"`
			Reason  string   `json:"reason"`
			Allowed []string `json:"allowed"`
		}
		_ = json.Unmarshal(result.Output, &detail)
		if result.Code != loop.Invalid || detail.Field != "bindings[0].target" || detail.Reason != "unknown_target" || !slices.Equal(detail.Allowed, []string{"garden"}) {
			t.Fatalf("uncorrectable target error: code=%s detail=%+v", result.Code, detail)
		}
	}
	p.steps = []turnStep{bindingCallStep("invented", `{"bindings":[{"target":"kitchen"}]}`), fix, {events: turnText("Bound.")}}
	got, err := ReplayIntakeModelCase(t.Context(), c, r.route)
	if err != nil || len(got) != 1 || got[0].Target.ID != "garden" {
		t.Fatalf("recovery: %+v %v", got, err)
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

// The model writes binding arguments as it reads them. A target it names by id
// or title, an excerpt it paraphrased, a message copy in the selector field and
// extra metadata must all bind; only an unknown or ambiguous item is corrected
// by the model.
func TestModelBindingContractTolerantArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(text string) string
		want binding.Target
	}{
		{"id-text", func(text string) string {
			return `{"input_event_id":"` + text + `","bindings":{"quote":"` + text + `","target":"garden"}}`
		}, binding.Target{Class: "existing", ID: "garden", Kind: "project"}},
		{"title-text", func(string) string {
			return `{"bindings":[{"target":"Garden","note":"ignored metadata"}]}`
		}, binding.Target{Class: "existing", ID: "garden", Kind: "project"}},
		{"new-kind", func(string) string {
			return `{"bindings":[{"quote":"New task.","target":{"class":"new","kind":"to-do"}}]}`
		}, binding.Target{Class: "new", Kind: "todo"}},
		{"focus", func(string) string {
			return `{"bindings":[{"target":{"class":"focus"}}]}`
		}, binding.Target{Class: "none"}},
		{"none", func(string) string {
			return `{"bindings":[{"target":{"class":"none","kind":""}}]}`
		}, binding.Target{Class: "none"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, p := turnRuntime(t)
			now := time.Now().UTC()
			c := intakereplay.Case{Version: 1, ID: "synthetic-tolerant", Set: "hard", State: &binding.State{Clock: now, Focus: binding.Target{Class: "existing", ID: "garden", Kind: "project"}, FocusAgeSeconds: 60, Questions: []binding.Question{}, Surfaces: map[string]string{}, Items: []binding.Item{{ID: "garden", Title: "Garden", Kind: "project", State: "active", LastActivity: now}}}, Input: binding.Input{Text: "Plant the beds."}, Gold: []intakereplay.Gold{{Target: binding.Target{Class: "new", Kind: "todo"}}}}
			p.steps = []turnStep{bindingCallStep("tolerant", tc.args("Plant the beds.")), {events: turnText("Bound."), before: func(req provider.Request) {
				if result := bindingToolResult(t, req, "tolerant"); result.Code != loop.OK {
					t.Fatalf("binding arguments rejected: %+v", result)
				}
			}}}
			got, err := ReplayIntakeModelCase(t.Context(), c, r.route)
			if err != nil || len(got) != 1 || got[0].Target != tc.want {
				t.Fatalf("binding: %+v %v", got, err)
			}
		})
	}
}

// A message with two requests gets one binding each even when the second target
// is a plain id and no request ordinals are sent.
func TestModelBindingContractMultipleRequestsWithoutOrdinals(t *testing.T) {
	r, p := turnRuntime(t)
	now := time.Now().UTC()
	text := "Check the garden. Start a new task."
	c := intakereplay.Case{Version: 1, ID: "synthetic-two-requests", Set: "hard", State: &binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Questions: []binding.Question{}, Surfaces: map[string]string{}, Items: []binding.Item{{ID: "garden", Title: "Garden", Kind: "project", State: "active", LastActivity: now}}}, Input: binding.Input{Text: text}, Gold: []intakereplay.Gold{{Target: binding.Target{Class: "existing", ID: "garden", Kind: "project"}}, {Target: binding.Target{Class: "new", Kind: "todo"}}}}
	args, _ := json.Marshal(map[string]any{"bindings": []any{
		map[string]any{"target": "garden"},
		map[string]any{"target": map[string]string{"class": "new", "kind": "todo"}, "difference": "Other work."},
	}})
	p.steps = []turnStep{bindingCallStep("two", string(args)), {events: turnText("Bound.")}}
	got, err := ReplayIntakeModelCase(t.Context(), c, r.route)
	if err != nil || len(got) != 2 {
		t.Fatalf("bindings: %+v %v", got, err)
	}
	if got[0].Target.ID != "garden" || got[1].Target.Class != "new" || got[1].Target.Kind != "todo" {
		t.Fatalf("two requests bound as %+v", got)
	}
	if intakereplay.Score(c, got).Passed != 1 {
		t.Fatalf("score: %+v", intakereplay.Score(c, got))
	}
}

// The work-tool selector accepts the message text and common selector names.
func TestModelBindingContractWorkSelectorShapes(t *testing.T) {
	r, _ := turnRuntime(t)
	create := questionPersonInput(t, r, "selector-create", "Save a garden task.")
	receipt := questionCall(t, modelBindingTools(t, r, create), "write_record", "create", map[string]any{"kind": "todo", "title": "Garden beds", "note": "Plant seeds."})
	if receipt.Code != loop.OK {
		t.Fatal(receipt)
	}
	var saved struct{ ID string }
	_ = json.Unmarshal(receipt.Output, &saved)
	input := questionPersonInput(t, r, "selector-shapes", "Check the garden beds.")
	for n, ref := range []any{
		map[string]any{"input": input.Body.Text(), "request": 0},
		map[string]any{"input_event_id": input.Body.Text()},
		map[string]any{"text": "current"},
		0,
		input.Body.Text(),
	} {
		tools := modelBindingTools(t, r, input)
		if result := questionCall(t, tools, "read_record", fmt.Sprint("shape-", n), map[string]any{"id": saved.ID, "input_binding": ref}); result.Code != loop.OK {
			t.Fatalf("selector shape %d rejected: %+v", n, result)
		}
	}
}
