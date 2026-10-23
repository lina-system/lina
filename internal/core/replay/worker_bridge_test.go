// SPDX-License-Identifier: Apache-2.0
package replay_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type runtimeStream struct{ done bool }

func (s *runtimeStream) Next(context.Context) (provider.Event, error) {
	if s.done {
		return provider.Event{}, io.EOF
	}
	s.done = true
	return provider.Event{Kind: provider.Terminal}, nil
}
func (*runtimeStream) Meta() provider.Meta { return provider.Meta{Class: provider.AgentRuntime} }
func (*runtimeStream) Close() error        { return nil }
func TestSandboxRuntimeBridgeUsesOnlyRecordedResults(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: "runtime", Name: "lookup", Arguments: `{}`}
	for _, recorded := range []bool{true, false} {
		cfg := replay.SandboxConfig{Executable: executable, Environment: []string{"PATH=" + os.Getenv("PATH")}, Stream: func(ctx context.Context, r provider.Request) (provider.Stream, error) {
			if r.Bridge == nil {
				t.Fatal("sandbox did not provide bridge")
			}
			result, err := r.Bridge.Call(ctx, call)
			if err != nil {
				return nil, err
			}
			if recorded && string(result.Output) != `{"id":"runtime","code":"ok","output":{"recorded":true}}` {
				t.Fatalf("result: %s", result.Output)
			}
			return &runtimeStream{}, nil
		}}
		in := replay.WorkerInput{RuntimeTools: true, Request: provider.Request{Tools: []provider.ToolDecl{[]byte(`{"type":"function","name":"lookup"}`)}}}
		if recorded {
			in.Calls = []replay.Call{{Call: call, Result: &loop.Result{Code: loop.OK, Output: []byte(`{"recorded":true}`)}}}
		}
		out, err := replay.Sandboxed(t.Context(), cfg, in)
		if recorded && (err != nil || len(out.Results) != 1) {
			t.Fatalf("%+v %v", out, err)
		}
		if !recorded && !errors.Is(err, replay.ErrExcluded) {
			t.Fatalf("unrecorded call: %v", err)
		}
	}
}

func TestSandboxRuntimeBridgeDispatchesNestedReads(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	expand := provider.ToolCall{ID: "expand-1", Name: "expand", Arguments: `{"id":"event"}`}
	recall := provider.ToolCall{ID: "recall-1", Name: "recall_person", Arguments: `{"query":"violet"}`}
	lookup := provider.ToolCall{ID: "runtime", Name: "lookup", Arguments: `{}`}
	seen := map[string]int{}
	readTool := func(output string) loop.Tool {
		return loop.Tool{
			Validate: func(provider.ToolCall) (string, error) { return "", nil },
			Execute: func(_ context.Context, call provider.ToolCall) loop.Result {
				seen[call.Name]++
				return loop.Result{ID: call.ID, Code: loop.OK, Output: []byte(output)}
			},
		}
	}
	cfg := replay.SandboxConfig{
		Executable:  executable,
		Environment: []string{"PATH=" + os.Getenv("PATH")},
		Reads: map[string]loop.Tool{
			"expand":        readTool(`{"events":[{"id":"event"}]}`),
			"recall_person": readTool(`{"memories":[{"text":"violet"}]}`),
		},
		Stream: func(ctx context.Context, r provider.Request) (provider.Stream, error) {
			if r.Bridge == nil {
				t.Fatal("sandbox did not provide bridge")
			}
			for _, call := range []provider.ToolCall{lookup, expand, recall} {
				result, err := r.Bridge.Call(ctx, call)
				if err != nil {
					return nil, err
				}
				switch call.Name {
				case "lookup":
					if string(result.Output) != `{"id":"runtime","code":"ok","output":{"recorded":true}}` {
						t.Fatalf("recorded result: %s", result.Output)
					}
				case "expand":
					if string(result.Output) != `{"id":"expand-1","code":"ok","output":{"events":[{"id":"event"}]}}` {
						t.Fatalf("expand result: %s", result.Output)
					}
				case "recall_person":
					if string(result.Output) != `{"id":"recall-1","code":"ok","output":{"memories":[{"text":"violet"}]}}` {
						t.Fatalf("recall result: %s", result.Output)
					}
				}
			}
			return &runtimeStream{}, nil
		},
	}
	in := replay.WorkerInput{
		RuntimeTools: true,
		Request: provider.Request{Tools: []provider.ToolDecl{
			[]byte(`{"type":"function","name":"lookup"}`),
			[]byte(`{"type":"function","name":"expand"}`),
			[]byte(`{"type":"function","name":"recall_person"}`),
		}},
		Calls: []replay.Call{{Call: lookup, Result: &loop.Result{Code: loop.OK, Output: []byte(`{"recorded":true}`)}}},
	}
	out, err := replay.Sandboxed(t.Context(), cfg, in)
	if err != nil || len(out.Results) != 3 || seen["expand"] != 1 || seen["recall_person"] != 1 {
		t.Fatalf("out=%+v seen=%v err=%v", out, seen, err)
	}
}

func TestSandboxRuntimeBridgeRejectsNestedWriteTarget(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: "expand-1", Name: "expand", Arguments: `{"id":"event"}`}
	executed := false
	cfg := replay.SandboxConfig{
		Executable:  executable,
		Environment: []string{"PATH=" + os.Getenv("PATH")},
		Reads: map[string]loop.Tool{"expand": {
			Validate: func(provider.ToolCall) (string, error) { return "workspace", nil },
			Execute: func(context.Context, provider.ToolCall) loop.Result {
				executed = true
				return loop.Result{Code: loop.OK}
			},
		}},
		Stream: func(ctx context.Context, r provider.Request) (provider.Stream, error) {
			_, err := r.Bridge.Call(ctx, call)
			return nil, err
		},
	}
	in := replay.WorkerInput{RuntimeTools: true, Request: provider.Request{Tools: []provider.ToolDecl{[]byte(`{"type":"function","name":"expand"}`)}}}
	_, err = replay.Sandboxed(t.Context(), cfg, in)
	if !errors.Is(err, replay.ErrInvalid) || executed {
		t.Fatalf("executed=%v err=%v", executed, err)
	}
}
