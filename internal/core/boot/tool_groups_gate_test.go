// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// A deferred work call through call_tool keeps the binding dependency of the
// tool it names: it starts only after bind_input committed in the same
// response. The binding call stays unfinished while the test holds it, and
// synctest.Wait returns only once every other goroutine is durably blocked.
func TestDeferredWorkWaitsForItsBinding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var committed, dispatched atomic.Bool
		entered, release := make(chan struct{}), make(chan struct{})
		bindings := map[string]loop.Tool{
			"bind_input": {
				Validate: func(provider.ToolCall) (string, error) { return "binding-gate:one", nil },
				Execute: func(context.Context, provider.ToolCall) loop.Result {
					close(entered)
					<-release
					committed.Store(true)
					return loop.Result{ID: "bind", Code: loop.OK}
				},
			},
			"worker_input": {
				Validate: func(provider.ToolCall) (string, error) { return "work:one", nil },
				Gate:     func(provider.ToolCall) string { return "binding-gate:one" },
				Execute: func(context.Context, provider.ToolCall) loop.Result {
					dispatched.Store(true)
					if !committed.Load() {
						t.Error("deferred work ran before the binding committed")
					}
					return loop.Result{ID: "deferred", Code: loop.OK}
				},
			},
		}
		all := []provider.ToolDecl{
			provider.ToolDecl(`{"type":"function","name":"bind_input","parameters":{"type":"object"}}`),
			provider.ToolDecl(`{"type":"function","name":"worker_input","parameters":{"type":"object"}}`),
		}
		catalog := prepareToolGroups(all, agents.WorkMethodSettings{Values: map[string]json.RawMessage{"tools.declare_first": []byte(`["work"]`)}}, bindings)
		stream := &oneRoundStream{calls: []provider.ToolCall{
			{ID: "bind", Name: "bind_input", Arguments: `{}`},
			{ID: "deferred", Name: "call_tool", Arguments: `{"name":"worker_input","arguments":{}}`},
		}}
		cfg := loop.Config{Tools: bindings, Stream: stream.stream, Request: provider.Request{Tools: catalog.first}}
		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := loop.Run(context.Background(), cfg); err != nil {
				t.Error(err)
			}
		}()
		<-entered
		synctest.Wait()
		if dispatched.Load() {
			t.Error("deferred work dispatched while the binding was unfinished")
		}
		close(release)
		<-done
		if !dispatched.Load() {
			t.Fatal("deferred work never ran after the binding committed")
		}
	})
}
