// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestToolGroupsOrderReachabilityAndReplay(t *testing.T) {
	all := []provider.ToolDecl{
		[]byte(`{"name":"create_timer"}`), []byte(`{"name":"calculate"}`), []byte(`{"name":"worker_start"}`), []byte(`{"name":"web_search"}`), []byte(`{"name":"web_extract"}`), []byte(`{"name":"observe_health"}`),
	}
	names := func(ds []provider.ToolDecl) []string {
		var n []string
		for _, d := range ds {
			n = append(n, declarationName(d))
		}
		return n
	}
	for _, tt := range []struct {
		groups string
		first  []string
	}{
		{`["work","device"]`, []string{"calculate", "load_tools", "call_tool", "worker_start", "observe_health"}},
		{`["calendar","web_research"]`, []string{"calculate", "load_tools", "call_tool", "create_timer", "web_search", "web_extract"}},
		{`[]`, []string{"calculate", "load_tools", "call_tool"}},
	} {
		t.Run(tt.groups, func(t *testing.T) {
			bindings := map[string]loop.Tool{}
			c := prepareToolGroups(all, agents.WorkMethodSettings{Values: map[string]json.RawMessage{"tools.declare_first": []byte(tt.groups)}}, bindings)
			if !reflect.DeepEqual(names(c.first), tt.first) {
				t.Fatal(names(c.first))
			}
			call := provider.ToolCall{ID: "load", Name: "load_tools", Arguments: `{"tools":["web_search","worker_start","observe_health","create_timer","calculate","web_extract"]}`}
			if _, err := bindings[call.Name].Validate(call); err != nil {
				t.Fatal(err)
			}
			result := bindings[call.Name].Execute(context.Background(), call)
			raw, _ := json.Marshal(result)
			item, _ := json.Marshal(map[string]string{"type": "function_call_output", "call_id": "load", "output": string(raw)})
			// A successful old-turn load is not reused. The current load is replayed
			// from persisted call/output items without executing the loader again.
			prior := provider.Item(`{"type":"function_call","call_id":"old","name":"load_tools"}`)
			c.offset = 2
			r := provider.Request{Items: []provider.Item{prior, item, []byte(`{"type":"function_call","call_id":"load","name":"load_tools"}`), item}}
			loaded, err := c.transform(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, n := range names(loaded.Tools) {
				if seen[n] {
					t.Fatal("duplicate", n)
				}
				seen[n] = true
			}
			for _, d := range all {
				if !seen[declarationName(d)] {
					t.Fatal("unreachable", string(d))
				}
			}
			if !reflect.DeepEqual(names(loaded.Tools[:len(c.first)]), tt.first) {
				t.Fatal("prefix changed")
			}
			r.Items = r.Items[:2]
			unloaded, _ := c.transform(t.Context(), r)
			if !reflect.DeepEqual(names(unloaded.Tools), tt.first) {
				t.Fatal("old turn leaked tools")
			}
			for _, args := range []string{`{"tools":["unknown"]}`, `{"tools":["calculate","calculate"]}`, `{"tools":[]}`, `{"tools":["calculate"],"grant":true}`} {
				call.Arguments = args
				if _, err := bindings[call.Name].Validate(call); err == nil {
					t.Fatal("invalid load accepted", args)
				}
			}
		})
	}
}

func TestLoadToolsDoesNotRepeatInitialSchemas(t *testing.T) {
	all := []provider.ToolDecl{
		[]byte(`{"name":"calculate","parameters":{"type":"object","required":["expression"]}}`),
		[]byte(`{"name":"worker_start","parameters":{"type":"object","required":["task_packet"]}}`),
		[]byte(`{"name":"web_search","parameters":{"type":"object","required":["query"]}}`),
	}
	bindings := map[string]loop.Tool{}
	catalog := prepareToolGroups(all, agents.WorkMethodSettings{Values: map[string]json.RawMessage{"tools.declare_first": []byte(`["work"]`)}}, bindings)
	result := bindings["load_tools"].Execute(t.Context(), provider.ToolCall{ID: "load", Name: "load_tools", Arguments: `{"tools":["calculate","worker_start","web_search"]}`})
	var loaded struct {
		Loaded       []string            `json:"loaded_tools"`
		Declarations []provider.ToolDecl `json:"declarations"`
	}
	if result.Code != loop.OK || json.Unmarshal(result.Output, &loaded) != nil {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(loaded.Loaded, []string{"calculate", "worker_start", "web_search"}) {
		t.Fatalf("lost requested tool acknowledgement: %v", loaded.Loaded)
	}
	if !reflect.DeepEqual(loaded.Declarations, all[2:]) {
		t.Fatalf("loader must return the deferred schema without duplicating initial schemas: %s", loaded.Declarations)
	}
	for _, want := range all {
		found := false
		for _, got := range append(append([]provider.ToolDecl{}, catalog.first...), loaded.Declarations...) {
			if reflect.DeepEqual(got, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("requested schema became unreachable: %s", want)
		}
	}
}

func TestDeferredRuntimeToolKeepsValidationAndAuthority(t *testing.T) {
	calls := 0
	bindings := map[string]loop.Tool{"worker_start": {Validate: func(c provider.ToolCall) (string, error) {
		if c.Arguments != `{"workdir":"allowed"}` {
			return "", tools.ErrInvalid
		}
		return "allowed", nil
	}, Execute: func(_ context.Context, c provider.ToolCall) loop.Result {
		calls++
		if c.Name != "worker_start" || c.ID != "invoke" {
			t.Fatal("lost call identity", c)
		}
		return loop.Result{ID: c.ID, Code: loop.Denied}
	}}}
	prepareToolGroups([]provider.ToolDecl{[]byte(`{"name":"worker_start"}`)}, agents.WorkMethodSettings{}, bindings)
	invoke, ok := bindings["call_tool"]
	if !ok {
		t.Fatal("agent-runtime has no on-demand invocation path")
	}
	for _, args := range []string{`{"name":"worker_start","arguments":{"workdir":"outside"}}`, `{"name":"unknown","arguments":{}}`} {
		c := provider.ToolCall{ID: "invoke", Name: "call_tool", Arguments: args}
		if _, err := invoke.Validate(c); err == nil {
			t.Fatal("accepted invalid nested tool", args)
		}
		if result := invoke.Execute(t.Context(), c); result.Code != loop.Invalid {
			t.Fatal(result)
		}
	}
	result := invoke.Execute(t.Context(), provider.ToolCall{ID: "invoke", Name: "call_tool", Arguments: `{"name":"worker_start","arguments":{"workdir":"allowed"}}`})
	if result.Code != loop.Denied || calls != 1 {
		t.Fatal("tool invocation bypassed authority", result, calls)
	}
}

// Completion evidence and channel checks wrap bindings after group selection.
// The runtime bridge must execute those final policy wrappers too.
func TestDeferredRuntimeToolKeepsFinalPolicyBinding(t *testing.T) {
	bindings := map[string]loop.Tool{"land_work_result": {
		Validate: func(provider.ToolCall) (string, error) { return "work", nil },
		Execute: func(_ context.Context, call provider.ToolCall) loop.Result {
			return loop.Result{ID: call.ID, Code: loop.OK}
		},
	}}
	prepareToolGroups([]provider.ToolDecl{provider.ToolDecl(`{"name":"land_work_result"}`)}, agents.WorkMethodSettings{}, bindings)
	binding := bindings["land_work_result"]
	binding.Execute = func(_ context.Context, call provider.ToolCall) loop.Result {
		return loop.Result{ID: call.ID, Code: loop.Denied}
	}
	bindings["land_work_result"] = binding
	got := bindings["call_tool"].Execute(t.Context(), provider.ToolCall{ID: "call", Name: "call_tool", Arguments: `{"name":"land_work_result","arguments":{}}`})
	if got.Code != loop.Denied {
		t.Fatalf("deferred bridge bypassed final policy: %s", got.Code)
	}
}

func TestWorkMethodRequestReplayKeepsPinnedRecord(t *testing.T) {
	method := `{"schema":1,"preset":"dev","workers":{"dispatch":true}}`
	input := assemble.Input{Core: assemble.PersonaCoreV1 + "\n" + assemble.TUIFormatting, AgentID: "dev", PersonaRevision: "commit", AgentName: "Dev", OwnerPersonID: "person", Persona: agents.Layers{Core: "fixture"}, WorkMethod: method, Current: []byte(`{"role":"user","content":"Make the result"}`), Model: "fixture-model", MaxTokens: 128000}
	request, report := assemble.Assemble(input, conversationRequestSize)
	if report.Failure != "" {
		t.Fatal(report.Failure)
	}
	ep := replay.Episode{WorkMethod: method, Persona: input.Persona, Items: request.Items, Evidence: ledger.RequestEvidence{Model: request.Model, Tools: request.Tools, Assembly: ledger.Assembly{AgentID: input.AgentID, AgentName: input.AgentName, OwnerPersonID: input.OwnerPersonID, InstructionFormat: "persona-tui-work-method-v1"}}}
	restored, err := replay.Request(ep, nil, "", "", replayInstructions)
	if err != nil {
		t.Fatal(err)
	}
	restored.Purpose = provider.Turn
	if assemble.Digest(restored) != assemble.Digest(request) {
		t.Fatal("work-method request cannot be reproduced from its pinned episode")
	}
	ep.WorkMethod = `{"schema":1,"preset":"dev","workers":{"dispatch":false}}`
	changed, err := replay.Request(ep, nil, "", "", replayInstructions)
	if err != nil {
		t.Fatal(err)
	}
	changed.Purpose = provider.Turn
	if assemble.Digest(changed) == assemble.Digest(request) {
		t.Fatal("request digest omitted work-method record")
	}
}
