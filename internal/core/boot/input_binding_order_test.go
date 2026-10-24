// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// oneRoundStream answers the first request with the given calls and ends every
// later request with an empty response.
type oneRoundStream struct {
	calls    []provider.ToolCall
	requests int
}

func (s *oneRoundStream) stream(context.Context, provider.Request) (provider.Stream, error) {
	s.requests++
	if s.requests > 1 {
		return &callStream{events: []provider.Event{{Kind: provider.Terminal}}}, nil
	}
	var events []provider.Event
	for i, c := range s.calls {
		item, _ := json.Marshal(map[string]string{"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": c.Arguments})
		call := c
		events = append(events, provider.Event{Kind: provider.ItemDone, Index: int64(i), Item: item}, provider.Event{Kind: provider.ToolCallReady, Index: int64(i), Call: &call})
	}
	return &callStream{events: append(events, provider.Event{Kind: provider.Terminal})}, nil
}

type callStream struct{ events []provider.Event }

func (s *callStream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*callStream) Meta() provider.Meta { return provider.Meta{} }
func (*callStream) Close() error        { return nil }

// One response binds a tied input and reads its work. The read must run after
// the binding committed its target question; without the barrier it reads the
// still-unresolved choice and is allowed.
func TestBindingCallCommitsBeforeTheWorkCallItOrders(t *testing.T) {
	r, _ := turnRuntime(t)
	for _, id := range []string{"first-task", "second-task"} {
		task := work.Task{ID: id, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: id, Done: "Done", ArtifactRoots: []string{t.TempDir()}}
		if err := r.work.Create(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		if err := r.work.Start(t.Context(), work.Run{ID: id + "-run", TaskID: id, Generation: 1, Worker: "codex", ThreadID: id + "-thread", TurnID: "old", NodeID: "node"}); err != nil {
			t.Fatal(err)
		}
	}
	input := questionPersonInput(t, r, "same-response", "Steer the running task.")
	tools := modelBindingTools(t, r, input)
	first := binding.Target{Class: "existing", Kind: "task", ID: "first-task"}
	second := binding.Target{Class: "existing", Kind: "task", ID: "second-task"}
	bind, _ := json.Marshal(map[string]any{"bindings": []any{map[string]any{"request": 0, "quote": input.Body.Text(), "target": first, "tied": []any{first, second}, "action": "steer"}}})
	calls := []provider.ToolCall{
		{ID: "bind", Name: "bind_input", Arguments: string(bind)},
		{ID: "progress", Name: "read_work_progress", Arguments: `{"id":"first-task-run"}`},
	}
	var orderMu sync.Mutex
	var order []string
	trace := func(name string) func(ctx context.Context, c provider.ToolCall) loop.Result {
		original := tools[name]
		return func(ctx context.Context, c provider.ToolCall) loop.Result {
			orderMu.Lock()
			order = append(order, name)
			orderMu.Unlock()
			return original.Execute(ctx, c)
		}
	}
	traced := map[string]loop.Tool{}
	for name, tool := range tools {
		tool.Execute = trace(name)
		traced[name] = tool
	}
	stream := &oneRoundStream{calls: calls}
	cfg := loop.Config{
		Tools:  traced,
		Stream: stream.stream,
		Request: provider.Request{Tools: []provider.ToolDecl{
			provider.ToolDecl(`{"type":"function","name":"bind_input","parameters":{"type":"object"}}`),
			provider.ToolDecl(`{"type":"function","name":"read_work_progress","parameters":{"type":"object"}}`),
		}},
	}
	out, err := loop.Run(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "bind_input" || order[1] != "read_work_progress" {
		t.Fatalf("execution order: %v", order)
	}
	if len(out.Results) != 2 || out.Results[0].Code != loop.OK {
		t.Fatalf("binding result: %+v", out.Results)
	}
	if out.Results[1].Code != loop.Denied || !strings.Contains(string(out.Results[1].Output), "input_target_pending") {
		t.Fatalf("work read ran on the unresolved choice: %+v", out.Results[1])
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || choices[0].Question == "" {
		t.Fatalf("target question not committed: %+v %v", choices, err)
	}
}
