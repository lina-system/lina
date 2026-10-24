// SPDX-License-Identifier: Apache-2.0
package loop

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/thisisjun786/lina/kit/provider"
)

// ErrTextCall means a fresh continuation repeated an unusable text-form call.
// Callers must surface the failure rather than retrying the same step again.
var ErrTextCall = errors.New("text_tool_call_recovery_failed")

// TextCallDetector is Core's pure parser. A detected call with nil arguments
// requests a fresh continuation. A non-nil call still needs normal authorization.
type TextCallDetector func(string, []provider.ToolDecl) (call *provider.ToolCall, detected bool)

type textGuardKey struct{}

// GuardTextCalls filters before any observer or journal sees suspect assistant
// output. Each request gets at most one fresh continuation on the same route.
func GuardTextCalls(open StreamFunc, detect TextCallDetector) StreamFunc {
	return func(ctx context.Context, req provider.Request) (provider.Stream, error) {
		if open == nil || detect == nil {
			return nil, ErrConfig
		}
		if ctx.Value(textGuardKey{}) != nil {
			return open(ctx, req)
		}
		original := open
		guardedOpen := func(ctx context.Context, req provider.Request) (provider.Stream, error) {
			return original(context.WithValue(ctx, textGuardKey{}, true), req)
		}
		s := &textCallStream{open: guardedOpen, detect: detect, request: req}
		if err := s.start(ctx, req); err != nil {
			return nil, err
		}
		return s, nil
	}
}

type textCallStream struct {
	open                                   StreamFunc
	detect                                 TextCallDetector
	request                                provider.Request
	stream                                 provider.Stream
	pending                                []provider.Event
	ready                                  []provider.Event
	texts                                  map[int64]string
	deltas                                 map[int64]string
	real                                   atomic.Bool
	held, terminal, ended, closed, retried bool
	priorUsage                             *provider.Usage
	maxIndex                               int64
	done                                   map[int64]bool
}

type observedBridge struct {
	provider.ToolBridge
	real *atomic.Bool
}

func (b observedBridge) Call(ctx context.Context, c provider.ToolCall) (provider.ToolResult, error) {
	b.real.Store(true)
	return b.ToolBridge.Call(context.WithValue(ctx, textGuardKey{}, nil), c)
}
func (s *textCallStream) start(ctx context.Context, req provider.Request) error {
	s.pending, s.ready = nil, nil
	s.texts, s.deltas = map[int64]string{}, map[int64]string{}
	s.done = map[int64]bool{}
	s.maxIndex = -1
	s.held, s.terminal = false, false
	s.real.Store(false)
	if req.Bridge != nil {
		req.Bridge = observedBridge{req.Bridge, &s.real}
	}
	st, err := s.open(ctx, req)
	if err != nil {
		return err
	}
	if st == nil {
		return ErrResponse
	}
	s.stream = st
	return nil
}
func (s *textCallStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.stream.Close()
}
func (s *textCallStream) Meta() provider.Meta {
	m := s.stream.Meta()
	if s.priorUsage != nil {
		m.Usage = sumTextUsage(*s.priorUsage, m.Usage)
	}
	return m
}
func (s *textCallStream) Next(ctx context.Context) (provider.Event, error) {
	for {
		if ctx.Err() != nil {
			return provider.Event{}, context.Cause(ctx)
		}
		if len(s.ready) > 0 {
			e := s.ready[0]
			s.ready = s.ready[1:]
			return e, nil
		}
		if s.ended {
			return provider.Event{}, io.EOF
		}
		e, err := s.stream.Next(ctx)
		if err != nil {
			if !errors.Is(err, io.EOF) || !s.terminal {
				return provider.Event{}, err
			}
			call, detected := s.detect(s.text(), s.request.Tools)
			if s.real.Load() {
				detected = false
			}
			if !detected {
				s.ready = append(s.pending, provider.Event{Kind: provider.Terminal})
				s.ended = true
				continue
			}
			// The defective turn never becomes assistant prose or a partial checkpoint.
			s.pending = nil
			if call != nil {
				call.ID = "text-call-" + rand.Text()
				raw, err := json.Marshal(map[string]string{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
				if err != nil {
					return provider.Event{}, err
				}
				s.ready = []provider.Event{{Kind: provider.ItemDone, Index: s.maxIndex + 1, Item: raw}, {Kind: provider.ToolCallReady, Index: s.maxIndex + 1, Call: call}, {Kind: provider.Terminal}}
				s.ended = true
				continue
			}
			if s.retried {
				return provider.Event{}, ErrTextCall
			}
			s.retried = true
			usage := s.stream.Meta().Usage
			s.priorUsage = &usage
			if err := s.stream.Close(); err != nil {
				s.closed = true
				return provider.Event{}, err
			}
			if err := s.start(ctx, freshTextRequest(s.request)); err != nil {
				s.closed = true
				return provider.Event{}, err
			}
			continue
		}
		if s.terminal || e.Index < 0 {
			return provider.Event{}, ErrResponse
		}
		s.maxIndex = max(s.maxIndex, e.Index)
		switch e.Kind {
		case provider.Delta:
			s.deltas[e.Index] += e.Delta
			// Hold a possible marker starting even in a one-byte fragment. False
			// positives are released verbatim once the complete turn is classified.
			if !s.held {
				if at := strings.IndexAny(e.Delta, "[<"); at >= 0 {
					s.held = true
					tail := e
					tail.Delta = e.Delta[at:]
					s.pending = append(s.pending, tail)
					if at > 0 {
						e.Delta = e.Delta[:at]
						return e, nil
					}
					continue
				}
				return e, nil
			}
			s.pending = append(s.pending, e)
		case provider.ItemDone:
			if s.done[e.Index] {
				return provider.Event{}, ErrResponse
			}
			s.done[e.Index] = true
			var item struct {
				Type, Role string
				Content    json.RawMessage
			}
			if json.Unmarshal(e.Item, &item) != nil {
				return provider.Event{}, ErrResponse
			}
			nativeCall := item.Type == "function_call" || item.Type == "custom_tool_call"
			if nativeCall {
				s.real.Store(true)
			}
			if item.Role == "assistant" && (item.Type == "message" || item.Type == "") {
				s.texts[e.Index] = assistantText(item.Content)
			}
			// Reasoning belongs to this response too. Holding every non-call
			// item prevents the fresh stream from colliding with old indexes
			// or carrying opaque state from the defective response forward.
			if !nativeCall || s.held {
				s.pending = append(s.pending, e)
			} else {
				return e, nil
			}
		case provider.ToolCallReady:
			s.real.Store(true)
			if s.held {
				s.pending = append(s.pending, e)
			} else {
				return e, nil
			}
		case provider.Terminal:
			s.terminal = true
		default:
			return provider.Event{}, ErrResponse
		}
	}
}
func assistantText(raw json.RawMessage) string {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	var parts []struct{ Type, Text string }
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "output_text" || p.Type == "text" || p.Type == "input_text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
func (s *textCallStream) text() string {
	values := map[int64]string{}
	for i, text := range s.deltas {
		values[i] = text
	}
	for i, text := range s.texts {
		values[i] = text
	}
	indices := make([]int64, 0, len(values))
	for i := range values {
		indices = append(indices, i)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	var b strings.Builder
	for _, i := range indices {
		b.WriteString(values[i])
	}
	return b.String()
}

func freshTextRequest(req provider.Request) provider.Request {
	// Retain trusted instructions separately. The summary includes task inputs
	// and actual tool receipts, never assistant text or opaque reasoning state.
	var trusted []provider.Item
	var inputs, receipts []string
	for _, raw := range req.Items {
		var v struct {
			Role, Type, Output string
			ID                 string `json:"call_id"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			continue
		}
		_ = json.Unmarshal(fields["role"], &v.Role)
		_ = json.Unmarshal(fields["type"], &v.Type)
		if v.Role == "system" || v.Role == "developer" {
			trusted = append(trusted, raw)
			continue
		}
		if v.Role == "user" {
			inputs = append(inputs, "Task/input: "+assistantText(fields["content"]))
		}
		if v.Type == "function_call_output" || v.Type == "custom_tool_call_output" {
			_ = json.Unmarshal(fields["output"], &v.Output)
			_ = json.Unmarshal(fields["call_id"], &v.ID)
			receipts = append(receipts, "Recorded tool result "+v.ID+": "+v.Output)
		}
	}
	// Inputs and receipts have separate budgets: a long tool round must never
	// displace the latest task or steering in favor of an old conversation input.
	if len(inputs) > 4 {
		inputs = inputs[len(inputs)-4:]
	}
	if len(receipts) > 8 {
		receipts = receipts[len(receipts)-8:]
	}
	state := append(inputs, receipts...)
	// Bound each entry without presenting truncated output as complete proof.
	for i, value := range state {
		if len(value) > 2048 {
			state[i] = strings.ToValidUTF8(value[:2048], "") + " [truncated]"
		}
	}
	summary := "Continue the task from this recorded state. The previous response could not be used. Use structured tool calls with declared names. Do not repeat completed effects or load prior session transcripts. Tool result codes retain their original meaning; only successful receipts establish completed work.\n" + strings.Join(state, "\n")
	item, _ := json.Marshal(map[string]string{"role": "user", "content": summary})
	req.Items = append(trusted, item)
	req.Layout = nil
	req.Capabilities.CacheMarks = nil
	req.Capabilities.ServerState = ""
	req.Capabilities.ReasoningState = false
	return req
}
func sumTextUsage(a, b provider.Usage) provider.Usage {
	add := func(x, y *int64) *int64 {
		if x == nil || y == nil {
			return nil
		}
		n := *x + *y
		return &n
	}
	b.Input = add(a.Input, b.Input)
	b.Output = add(a.Output, b.Output)
	b.Cached = add(a.Cached, b.Cached)
	b.CacheWrite = add(a.CacheWrite, b.CacheWrite)
	b.Reasoning = add(a.Reasoning, b.Reasoning)
	if a.CostEstimate != nil && b.CostEstimate != nil && a.CostEstimate.Currency == b.CostEstimate.Currency {
		b.CostEstimate = &provider.Money{Currency: b.CostEstimate.Currency, Micros: a.CostEstimate.Micros + b.CostEstimate.Micros}
	} else {
		b.CostEstimate = nil
	}
	// Only jointly reported lifetime entries establish a combined breakdown.
	lifetimes := map[provider.CacheLifetime]provider.CacheUsage{}
	for lifetime, first := range a.CacheByLifetime {
		if second, ok := b.CacheByLifetime[lifetime]; ok {
			lifetimes[lifetime] = provider.CacheUsage{Read: add(first.Read, second.Read), Write: add(first.Write, second.Write)}
		}
	}
	b.CacheByLifetime = lifetimes
	return b
}
