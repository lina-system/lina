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
