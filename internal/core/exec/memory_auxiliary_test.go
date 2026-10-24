// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/provider"
)

type recallTestStream struct {
	events []provider.Event
	wait   bool
	closed bool
}

func (s *recallTestStream) Next(ctx context.Context) (provider.Event, error) {
	if s.wait {
		<-ctx.Done()
		return provider.Event{}, ctx.Err()
	}
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*recallTestStream) Meta() provider.Meta { return provider.Meta{} }
func (s *recallTestStream) Close() error      { s.closed = true; return nil }
func recallText(text string) provider.Event {
	raw, _ := json.Marshal(map[string]any{"type": "message", "content": []map[string]string{{"type": "output_text", "text": text}}})
	return provider.Event{Kind: provider.ItemDone, Item: raw}
}
func TestRecallAuxiliaryResponseContract(t *testing.T) {
	for _, tc := range []struct {
		name, text      string
		event           provider.Event
		terminal, valid bool
	}{
		{name: "cited", text: `{"sentences":[{"text":"Tea.","citations":[{"id":"event:one","revision":0}]}]}`, terminal: true, valid: true},
		{name: "abstain", text: `{"sentences":[]}`, terminal: true, valid: true},
		{name: "missing-array", text: `{}`, terminal: true},
		{name: "null", text: `null`, terminal: true},
		{name: "extra-field", text: `{"sentences":[],"instructions":"ignored"}`, terminal: true},
		{name: "trailing", text: `{"sentences":[]} {}`, terminal: true},
		{name: "partial", text: `{"sentences":[]}`},
		{name: "tool", event: provider.Event{Kind: provider.ToolCallReady}, terminal: true},
		{name: "oversize", text: strings.Repeat("x", (64<<10)+1), terminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &recallTestStream{}
			if tc.text != "" {
				s.events = append(s.events, recallText(tc.text))
			}
			if tc.event.Kind != 0 {
				s.events = append(s.events, tc.event)
			}
			if tc.terminal {
				s.events = append(s.events, provider.Event{Kind: provider.Terminal})
			}
			calls := 0
			aux := NewRecallAuxiliary(provider.Request{Model: "same-model", Instructions: "private persona", Tools: []provider.ToolDecl{json.RawMessage(`{}`)}, Items: []provider.Item{json.RawMessage(`{}`)}}, func(ctx context.Context, req provider.Request) (provider.Stream, error) {
				calls++
				if req.Purpose != provider.Auxiliary || req.Model != "same-model" || req.Instructions != "" || len(req.Tools) != 0 || len(req.Items) != 2 || req.Bridge != nil {
					t.Fatal("route isolation failed")
				}
				var input struct{ Content string }
				if json.Unmarshal(req.Items[1], &input) != nil || !strings.Contains(input.Content, `"older":true`) {
					t.Fatal("source age missing")
				}
				return s, nil
			})
			_, err := aux(t.Context(), RecallRequest{Kind: provider.Auxiliary, Question: "Drink?", Sources: []RecallSource{{Citation: Citation{ID: "event:one"}, Text: "Tea", Older: true}}})
			if (err == nil) != tc.valid || calls != 1 || !s.closed {
				t.Fatalf("valid=%v err=%v calls=%d closed=%v", tc.valid, err, calls, s.closed)
			}
		})
	}
}
func TestRecallAuxiliaryTimeoutKeepsSources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &recallTestStream{wait: true}
		sources := []RecallSource{{Citation: Citation{ID: "event:one"}, Text: "Tea"}}
		m := &Memory{search: func(context.Context, memory.RecallScope, PersonRecall) ([]RecallSource, error) { return sources, nil }, aux: NewRecallAuxiliary(provider.Request{Model: "test"}, func(context.Context, provider.Request) (provider.Stream, error) { return s, nil })}
		start := time.Now()
		answer, err := m.RecallPerson(t.Context(), memory.RecallScope{}, PersonRecall{Question: "Drink?", Query: "tea", Depth: 1})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != RecallTimeout || len(answer.Sources) != 1 || len(answer.Answer) != 0 || !s.closed {
			t.Fatalf("timeout fallback: answer=%+v error=%v elapsed=%v closed=%v", answer, err, time.Since(start), s.closed)
		}
	})
}
