// SPDX-License-Identifier: Apache-2.0
package textcall_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/textcall"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type wire struct {
	events []provider.Event
	closed bool
	err    error
	meta   provider.Meta
}

func (w *wire) Next(context.Context) (provider.Event, error) {
	if len(w.events) == 0 {
		if w.err != nil {
			return provider.Event{}, w.err
		}
		return provider.Event{}, io.EOF
	}
	e := w.events[0]
	w.events = w.events[1:]
	return e, nil
}
func (w *wire) Meta() provider.Meta { return w.meta }
func (w *wire) Close() error        { w.closed = true; return nil }
func message(text string) *wire {
	raw, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": text}}})
	w := &wire{}
	for _, r := range text {
		w.events = append(w.events, provider.Event{Kind: provider.Delta, Delta: string(r)})
	}
	w.events = append(w.events, provider.Event{Kind: provider.ItemDone, Item: raw}, provider.Event{Kind: provider.Terminal})
	return w
}

var declaration = provider.ToolDecl(`{"type":"function","name":"read_skill","parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}}`)

const complete = "[tool_use id=test name=_read_skill]\n{\"id\":\"fixture\"}"

func TestRecoveredCallUsesOrdinaryHooksAndJournal(t *testing.T) {
	for _, code := range []loop.Code{loop.OK, loop.Denied, loop.AwaitingPerson} {
		t.Run(string(code), func(t *testing.T) {
			requests, executions := 0, 0
			var order []string
			var streams []*wire
			cfg := loop.Config{Request: provider.Request{Tools: []provider.ToolDecl{declaration}}, Tools: map[string]loop.Tool{"read_skill": {
				Validate: func(c provider.ToolCall) (string, error) {
					order = append(order, "validate")
					if c.Name != "read_skill" || c.Arguments != `{"id":"fixture"}` {
						t.Errorf("call=%+v", c)
					}
					return "", nil
				},
				Execute: func(_ context.Context, c provider.ToolCall) loop.Result {
					executions++
					order = append(order, "execute")
					return loop.Result{ID: c.ID, Code: code}
				},
			}}}
			cfg.Stream = textcall.Guard(func(_ context.Context, req provider.Request) (provider.Stream, error) {
				requests++
				for _, i := range req.Items {
					if strings.Contains(string(i), "[tool_use") {
						t.Fatal("contaminated history")
					}
				}
				text := "Done."
				if requests == 1 {
					text = complete
				}
				w := message(text)
				streams = append(streams, w)
				return w, nil
			})
			cfg.Before = func(_ context.Context, c provider.ToolCall) *loop.Result {
				order = append(order, "before")
				if code == loop.Denied {
					return &loop.Result{ID: c.ID, Code: code}
				}
				return nil
			}
			cfg.After = func(_ context.Context, _ provider.ToolCall, r loop.Result) loop.Result {
				order = append(order, "after")
				return r
			}
			cfg.Observe = func(_ context.Context, e provider.Event) error {
				if strings.Contains(e.Delta, "[tool_use") || strings.Contains(string(e.Item), "[tool_use") {
					t.Error("text leaked")
				}
				return nil
			}
			cfg.Journal.Item = func(_ context.Context, _ int, _ int64, i provider.Item) error {
				if strings.Contains(string(i), "[tool_use") {
					t.Error("text journaled")
				}
				order = append(order, "item")
				return nil
			}
			cfg.Journal.Calls = func(context.Context, int, []provider.ToolCall) error { order = append(order, "calls"); return nil }
			cfg.Journal.Start = func(context.Context, int, int, provider.ToolCall) error { order = append(order, "start"); return nil }
			cfg.Journal.Result = func(context.Context, int, int, loop.Result) error { order = append(order, "result"); return nil }
			out, err := loop.Run(t.Context(), cfg)
			if err != nil || requests != 2 || len(out.Results) != 1 || out.Results[0].Code != code {
				t.Fatalf("out=%+v err=%v requests=%d", out, err, requests)
			}
			want := 1
			if code == loop.Denied {
				want = 0
			}
			if executions != want {
				t.Errorf("executions=%d", executions)
			}
			positions := map[string]int{}
			for i, s := range order {
				if _, ok := positions[s]; !ok {
					positions[s] = i
				}
			}
			if positions["item"] >= positions["calls"] || positions["calls"] >= positions["before"] || positions["before"] >= positions["result"] {
				t.Errorf("order=%v", order)
			}
			for _, w := range streams {
				if !w.closed {
					t.Error("stream not closed")
				}
			}
		})
	}
}
func TestFreshContinuationPreservesRouteAndVerifiedState(t *testing.T) {
	for _, bad := range []string{"[tool_use", "[tool_use name=read_skill]\n{broken}", "[tool_use name=missing]\n{}"} {
		t.Run(bad, func(t *testing.T) {
			for _, repeat := range []bool{false, true} {
				attempts := 0
				req := provider.Request{Model: "same-model", Effort: "high", Purpose: provider.Auxiliary, Credential: provider.NewCredential("same-account", provider.APIKey, "synthetic"), Instructions: "trusted rules", Tools: []provider.ToolDecl{declaration}, Capabilities: provider.CapabilityUse{ServerState: "old-session", ReasoningState: true}, Items: []provider.Item{
					json.RawMessage(`{"role":"user","content":"Read the fixture"}`), json.RawMessage(`{"role":"assistant","content":"old assistant content"}`), json.RawMessage(`{"type":"function_call_output","call_id":"verified","output":"{\"code\":\"ok\",\"output\":\"verified result\"}"}`),
				}}
				open := textcall.Guard(func(_ context.Context, q provider.Request) (provider.Stream, error) {
					attempts++
					if q.Model != req.Model || q.Effort != req.Effort || q.Credential.Account != req.Credential.Account || q.Purpose != req.Purpose || q.Instructions != req.Instructions || !reflect.DeepEqual(q.Tools, req.Tools) {
						t.Error("route or declarations changed")
					}
					if attempts == 2 {
						raw, _ := json.Marshal(q.Items)
						s := string(raw)
						if strings.Contains(s, "old assistant") || strings.Contains(s, "[tool_use") || !strings.Contains(s, "Read the fixture") || !strings.Contains(s, "verified result") || q.Capabilities.ServerState != "" || q.Capabilities.ReasoningState {
							t.Errorf("fresh state=%s capabilities=%+v", s, q.Capabilities)
						}
					}
					text := "Recovered."
					if attempts == 1 || repeat {
						text = bad
					}
					return message(text), nil
				})
				// Duplicate installation must still permit only two underlying requests.
				out, err := loop.Run(t.Context(), loop.Config{Request: req, Stream: textcall.Guard(open)})
				if attempts != 2 || (repeat && !errors.Is(err, loop.ErrTextCall)) || (!repeat && err != nil) || len(out.Results) != 0 {
					t.Fatalf("attempts=%d err=%v out=%+v", attempts, err, out)
				}
			}
		})
	}
}
func TestNormalProseAndStructuredCallsRemainVerbatim(t *testing.T) {
	for _, text := range []string{"Normal prose.", "Mention `[tool_use` in prose.", "```\n" + complete + "\n```", "<thinking>" + complete + "</thinking>\nAn answer.", complete} {
		t.Run(text, func(t *testing.T) {
			w := message(text)
			if text == complete {
				call := provider.ToolCall{ID: "real", Name: "read_skill", Arguments: `{"id":"fixture"}`}
				raw, _ := json.Marshal(map[string]string{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
				n := len(w.events) - 1
				w.events = append(w.events[:n], provider.Event{Kind: provider.ItemDone, Index: 1, Item: raw}, provider.Event{Kind: provider.ToolCallReady, Index: 1, Call: &call}, provider.Event{Kind: provider.Terminal})
			}
			original := append([]provider.Event(nil), w.events...)
			s, err := textcall.Guard(func(context.Context, provider.Request) (provider.Stream, error) { return w, nil })(t.Context(), provider.Request{Tools: []provider.ToolDecl{declaration}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var deltas strings.Builder
			var items []provider.Item
			for {
				e, err := s.Next(t.Context())
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if e.Kind == provider.Delta {
					deltas.WriteString(e.Delta)
				}
				if e.Kind == provider.ItemDone {
					items = append(items, e.Item)
				}
			}
			var expected []provider.Item
			for _, e := range original {
				if e.Kind == provider.ItemDone {
					expected = append(expected, e.Item)
				}
			}
			if deltas.String() != text || !reflect.DeepEqual(items, expected) {
				t.Fatalf("text/items changed: %q %s", deltas.String(), items)
			}
		})
	}
}
func TestIncompleteTransportDoesNotRecoverOrLeakCandidate(t *testing.T) {
	w := message(complete)
	w.events = w.events[:len(w.events)-1]
	w.err = io.ErrUnexpectedEOF
	requests := 0
	s, err := textcall.Guard(func(context.Context, provider.Request) (provider.Stream, error) { requests++; return w, nil })(t.Context(), provider.Request{Tools: []provider.ToolDecl{declaration}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for {
		e, err := s.Next(t.Context())
		if err != nil {
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal(err)
			}
			break
		}
		if e.Kind == provider.Delta || e.Kind == provider.ItemDone {
			t.Errorf("candidate escaped on cutoff: %+v", e)
		}
	}
	if requests != 1 {
		t.Errorf("requests=%d", requests)
	}
}

type nativeWire struct {
	*wire
	request provider.Request
	called  bool
}

func (w *nativeWire) Next(ctx context.Context) (provider.Event, error) {
	if !w.called {
		w.called = true
		if _, err := w.request.Bridge.Call(ctx, provider.ToolCall{ID: "native", Name: "read_skill", Arguments: `{"id":"fixture"}`}); err != nil {
			return provider.Event{}, err
		}
	}
	return w.wire.Next(ctx)
}
func TestNativeRuntimeCallPreemptsTextRecovery(t *testing.T) {
	requests, calls := 0, 0
	cfg := loop.Config{RuntimeTools: true, Request: provider.Request{Tools: []provider.ToolDecl{declaration}}, Tools: map[string]loop.Tool{"read_skill": {Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(_ context.Context, c provider.ToolCall) loop.Result {
		calls++
		return loop.Result{ID: c.ID, Code: loop.OK}
	}}}}
	cfg.Stream = textcall.Guard(func(_ context.Context, q provider.Request) (provider.Stream, error) {
		requests++
		return &nativeWire{wire: message(complete), request: q}, nil
	})
	out, err := loop.Run(t.Context(), cfg)
	if err != nil || requests != 1 || calls != 1 || len(out.Results) != 1 {
		t.Fatalf("requests=%d calls=%d out=%+v err=%v", requests, calls, out, err)
	}
}

func TestFreshContinuationClosesFirstStreamAndAccountsBothAttempts(t *testing.T) {
	n := int64(7)
	first, second := message("[tool_use"), message("Recovered.")
	for _, w := range []*wire{first, second} {
		w.meta = provider.Meta{Usage: provider.Usage{Input: &n, Output: &n, CacheByLifetime: map[provider.CacheLifetime]provider.CacheUsage{provider.Cache5Minutes: {Read: &n}}}}
	}
	requests := 0
	s, err := textcall.Guard(func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		if requests == 1 {
			return first, nil
		}
		if !first.closed {
			t.Error("old session still open")
		}
		return second, nil
	})(t.Context(), provider.Request{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for {
		_, err := s.Next(t.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	u := s.Meta().Usage
	if u.Input == nil || *u.Input != 14 || u.Output == nil || *u.Output != 14 || u.Cached != nil {
		t.Fatalf("usage=%+v", u)
	}
	if got := u.CacheByLifetime[provider.Cache5Minutes].Read; got == nil || *got != 14 {
		t.Fatalf("lifetime usage=%+v", u.CacheByLifetime)
	}
}
func TestCancelledTextStreamNeverStartsContinuation(t *testing.T) {
	requests := 0
	ctx, cancel := context.WithCancel(t.Context())
	s, err := textcall.Guard(func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		return message("[tool_use"), nil
	})(ctx, provider.Request{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cancel()
	if _, err = s.Next(ctx); !errors.Is(err, context.Canceled) || requests != 1 {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestFreshContinuationDropsPriorReasoningItems(t *testing.T) {
	requests := 0
	out, err := loop.Run(t.Context(), loop.Config{Stream: textcall.Guard(func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		if requests == 1 {
			w := message("[tool_use")
			for i := range w.events {
				w.events[i].Index = 1
			}
			w.events = append([]provider.Event{{Kind: provider.ItemDone, Index: 0, Item: json.RawMessage(`{"type":"reasoning","id":"old-reasoning","summary":[]}`)}}, w.events...)
			return w, nil
		}
		return message("Recovered."), nil
	})})
	if err != nil || requests != 2 {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
	for _, item := range out.Items {
		if strings.Contains(string(item), "old-reasoning") {
			t.Errorf("contaminated reasoning retained: %s", item)
		}
	}
}

func TestFreshContinuationKeepsTaskSeparateFromReceiptBudget(t *testing.T) {
	items := []provider.Item{json.RawMessage(`{"role":"user","content":"unrelated old task"}`), json.RawMessage(`{"role":"user","content":"CURRENT TASK: inspect the fixture"}`), json.RawMessage(`{"role":"user","content":"STEERING: preserve the evidence"}`)}
	for range 12 {
		items = append(items, json.RawMessage(`{"type":"function_call_output","call_id":"verified","output":"{\"code\":\"ok\",\"output\":\"receipt\"}"}`))
	}
	requests := 0
	_, err := loop.Run(t.Context(), loop.Config{Request: provider.Request{Items: items}, Stream: textcall.Guard(func(_ context.Context, q provider.Request) (provider.Stream, error) {
		requests++
		if requests == 1 {
			return message("[tool_use"), nil
		}
		raw, _ := json.Marshal(q.Items)
		if !strings.Contains(string(raw), "CURRENT TASK") || !strings.Contains(string(raw), "STEERING") {
			t.Errorf("active task or steering lost: %s", raw)
		}
		return message("Recovered."), nil
	})})
	if err != nil || requests != 2 {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}
