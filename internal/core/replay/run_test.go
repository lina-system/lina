// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type stream struct{ events []provider.Event }

func (s *stream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*stream) Close() error        { return nil }
func (*stream) Meta() provider.Meta { return provider.Meta{Class: provider.ModelAPI} }

func TestReplayUsesExactRecordedResultAndNeverAnEffectExecutor(t *testing.T) {
	call := provider.ToolCall{ID: "original", Name: "write_file", Arguments: `{"path":"note","content":"violet"}`}
	answer := loop.Result{ID: call.ID, Code: loop.OK, Output: json.RawMessage(`{"receipt":"recorded"}`)}
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "changed arguments excluded"}[changed], func(t *testing.T) {
			rounds := 0
			cfg := RunConfig{Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"write_file"}`)}}, Calls: []Call{{Call: call, Result: &answer}}}
			cfg.Stream = func(_ context.Context, r provider.Request) (provider.Stream, error) {
				rounds++
				if r.Purpose != provider.Replay {
					t.Fatal("wrong usage kind")
				}
				if rounds == 1 {
					c := call
					c.ID = "candidate"
					if changed {
						c.Arguments = `{"path":"different","content":"violet"}`
					}
					raw, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": c.Arguments})
					return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: raw}, {Kind: provider.ToolCallReady, Call: &c}, {Kind: provider.Terminal}}}, nil
				}
				raw, _ := json.Marshal(r.Items)
				if !strings.Contains(string(raw), "recorded") || !strings.Contains(string(raw), "candidate") {
					t.Fatal("recorded result was not supplied")
				}
				return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: json.RawMessage(`{"type":"message","role":"assistant","content":[]}`)}, {Kind: provider.Terminal}}}, nil
			}
			out, err := Run(t.Context(), cfg)
			if changed {
				if !errors.Is(err, ErrExcluded) || rounds != 1 {
					t.Fatalf("rounds=%d err=%v", rounds, err)
				}
			} else if err != nil || len(out.Results) != 1 || rounds != 2 {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
}
func TestUnrecordedEffectExcludesBeforeModelCall(t *testing.T) {
	called := false
	_, err := Run(t.Context(), RunConfig{Calls: []Call{{Call: provider.ToolCall{Name: "send", ID: "call", Arguments: `{}`}}}, Stream: func(context.Context, provider.Request) (provider.Stream, error) { called = true; return nil, nil }})
	if !errors.Is(err, ErrExcluded) || called {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

func TestExportMasksSecretKeysInRecordedJSON(t *testing.T) {
	raw, err := maskJSON([]byte(`{"api_key":"syntheticcredential","nested":{"password":"anothercredential"},"output":"token=thirdcredential"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"syntheticcredential", "anothercredential", "thirdcredential"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("recorded JSON secret leaked")
		}
	}
	if !json.Valid(raw) {
		t.Fatal("masking broke JSON")
	}
}

func TestReplayLocalReadRunsLiveWithoutRecordedResult(t *testing.T) {
	readCalls, rounds := 0, 0
	call := provider.ToolCall{ID: "read", Name: "expand", Arguments: `{"id":"event"}`}
	tool := loop.Tool{Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(context.Context, provider.ToolCall) loop.Result {
		readCalls++
		return loop.Result{ID: call.ID, Code: loop.OK, Output: json.RawMessage(`{"text":"current visible source"}`)}
	}}
	_, err := Run(t.Context(), RunConfig{Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"expand"}`)}}, Calls: []Call{{Call: call}}, Reads: map[string]loop.Tool{"expand": tool}, Stream: func(_ context.Context, r provider.Request) (provider.Stream, error) {
		rounds++
		if rounds == 1 {
			raw, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
			return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: raw}, {Kind: provider.ToolCallReady, Call: &call}, {Kind: provider.Terminal}}}, nil
		}
		raw, _ := json.Marshal(r.Items)
		if !strings.Contains(string(raw), "current visible source") {
			t.Fatal("live result missing")
		}
		return &stream{[]provider.Event{{Kind: provider.Terminal}}}, nil
	}})
	if err != nil || readCalls != 1 || rounds != 2 {
		t.Fatalf("reads=%d rounds=%d err=%v", readCalls, rounds, err)
	}
}

func TestReplayRuntimeUsesRecordedBridgeResults(t *testing.T) {
	call := provider.ToolCall{ID: "runtime", Name: "lookup", Arguments: `{}`}
	out, err := Run(t.Context(), RunConfig{RuntimeTools: true, Request: provider.Request{Tools: []provider.ToolDecl{[]byte(`{"type":"function","name":"lookup"}`)}}, Calls: []Call{{Call: call, Result: &loop.Result{Code: loop.OK, Output: []byte(`{"recorded":true}`)}}}, Stream: func(ctx context.Context, r provider.Request) (provider.Stream, error) {
		if r.Bridge == nil {
			t.Fatal("missing runtime bridge")
		}
		result, err := r.Bridge.Call(ctx, call)
		if err != nil || !strings.Contains(string(result.Output), "recorded") {
			t.Fatalf("%s %v", result.Output, err)
		}
		return &stream{[]provider.Event{{Kind: provider.Terminal}}}, nil
	}})
	if err != nil || len(out.Results) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestReplayRestoresLoadedDeclarationsAndEnabledTools(t *testing.T) {
	initial := []provider.ToolDecl{[]byte(`{"name":"load_tools"}`)}
	load := provider.ToolCall{ID: "load", Name: "load_tools", Arguments: `{"tools":["web_search"]}`}
	result := loop.Result{ID: load.ID, Code: loop.OK, Output: []byte(`{"loaded_tools":["web_search"],"declarations":[{"name":"web_search","parameters":{"type":"object"}}],"declaration_order":["web_search"]}`)}
	rounds := 0
	out, err := Run(t.Context(), RunConfig{Request: provider.Request{Tools: initial, Instructions: "prefix\n<enabled_tools>\n[]\n</enabled_tools>\nsuffix"}, Calls: []Call{{Call: load, Result: &result}}, Stream: func(_ context.Context, r provider.Request) (provider.Stream, error) {
		rounds++
		if rounds == 1 {
			raw, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": load.ID, "name": load.Name, "arguments": load.Arguments})
			return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: raw}, {Kind: provider.ToolCallReady, Call: &load}, {Kind: provider.Terminal}}}, nil
		}
		if len(r.Tools) != 2 || !strings.Contains(string(r.Tools[1]), "web_search") || !strings.Contains(r.Instructions, "web_search") {
			t.Errorf("loaded tool missing: tools=%s instructions=%s", r.Tools, r.Instructions)
		}
		return &stream{[]provider.Event{{Kind: provider.Terminal}}}, nil
	}})
	if err != nil || rounds != 2 || len(out.Results) != 1 {
		t.Fatalf("out=%+v rounds=%d err=%v", out, rounds, err)
	}
}

func TestReplayLoadsOnlyCurrentSuccessfulResultsInRegistryOrder(t *testing.T) {
	initial := []provider.ToolDecl{[]byte(`{"name":"load_tools"}`)}
	makeCall := func(id, args string, code loop.Code, output string) Call {
		return Call{Call: provider.ToolCall{ID: id, Name: "load_tools", Arguments: args}, Result: &loop.Result{ID: id, Code: code, Output: []byte(output)}}
	}
	calls := []Call{
		makeCall("failed", `{"tools":["a"]}`, loop.Denied, `{"loaded_tools":["a"],"declarations":[{"name":"a"}]}`),
		makeCall("z", `{"tools":["z"]}`, loop.OK, `{"loaded_tools":["z"],"declarations":[{"name":"z"}],"declaration_order":["a","z"]}`),
		makeCall("a", `{"tools":["a"]}`, loop.OK, `{"loaded_tools":["a"],"declarations":[{"name":"a"}],"declaration_order":["a","z"]}`),
	}
	oldResult, _ := json.Marshal(calls[2].Result)
	oldOutput, _ := json.Marshal(map[string]string{"type": "function_call_output", "call_id": "old", "output": string(oldResult)})
	history := []provider.Item{[]byte(`{"type":"function_call","name":"load_tools","call_id":"old"}`), oldOutput}
	round := 0
	want := [][]string{{"load_tools"}, {"load_tools"}, {"load_tools", "z"}, {"load_tools", "a", "z"}}
	_, err := Run(t.Context(), RunConfig{Request: provider.Request{Tools: initial, Items: history}, Calls: calls, Stream: func(_ context.Context, r provider.Request) (provider.Stream, error) {
		var names []string
		for _, raw := range r.Tools {
			var d struct{ Name string }
			_ = json.Unmarshal(raw, &d)
			names = append(names, d.Name)
		}
		if !reflect.DeepEqual(names, want[round]) {
			t.Errorf("round %d tools=%v want=%v", round, names, want[round])
		}
		if round == len(calls) {
			round++
			return &stream{[]provider.Event{{Kind: provider.Terminal}}}, nil
		}
		c := calls[round].Call
		round++
		raw, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": c.Arguments})
		return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: raw}, {Kind: provider.ToolCallReady, Call: &c}, {Kind: provider.Terminal}}}, nil
	}})
	if err != nil || round != 4 {
		t.Fatalf("round=%d error=%v", round, err)
	}
}

func TestReplayExcludesAmbiguousLegacyCatalog(t *testing.T) {
	called := false
	_, err := Run(t.Context(), RunConfig{Calls: []Call{{Call: provider.ToolCall{Name: "load_tools", Arguments: `{"tools":["z","a"]}`}, Result: &loop.Result{Code: loop.OK, Output: []byte(`{"loaded_tools":["z","a"],"declarations":[{"name":"z"},{"name":"a"}]}`)}}}, Stream: func(context.Context, provider.Request) (provider.Stream, error) { called = true; return nil, nil }})
	if !errors.Is(err, ErrExcluded) || called {
		t.Fatalf("ambiguous order sent to provider: called=%v err=%v", called, err)
	}
}
