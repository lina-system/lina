// SPDX-License-Identifier: Apache-2.0
package exchange_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type knowledge struct {
	t     *testing.T
	reads []string
}

func (k *knowledge) Persona(_ context.Context, turn, agent string) (agents.Preparation, error) {
	k.reads = append(k.reads, "persona")
	if agent != "answerer" || turn != "delivery-1" {
		k.t.Fatalf("persona scope: %s %s", turn, agent)
	}
	return agents.Preparation{TurnID: turn, AgentID: agent, Revision: 1, Commit: "revision", Layers: agents.Layers{Core: "persona-core", DefaultVoice: "default-voice", UserVoice: "user-voice", GrowthSnapshot: "growth"}}, nil
}
func (k *knowledge) Common(context.Context) ([]string, error) {
	k.reads = append(k.reads, "common")
	return []string{"shared-fact"}, nil
}
func (k *knowledge) Material(_ context.Context, m exchange.Material) (string, error) {
	k.reads = append(k.reads, "material")
	if m != (exchange.Material{ID: "draft", Revision: "revision-1"}) {
		k.t.Fatalf("unnamed material: %+v", m)
	}
	return "named-material", nil
}
func (k *knowledge) Personal(context.Context, string) ([]string, error) {
	k.t.Fatal("personal knowledge read")
	return nil, nil
}
func TestExchangePreparationReadsOnlyAllowedKnowledge(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	k := &knowledge{t: t}
	p, err := s.Prepare(t.Context(), r.DeliveryID, "answerer", now, k)
	if err != nil {
		t.Fatal(err)
	}
	if p.Agent != "answerer" || p.ExchangeID != r.ID || p.Request.Purpose != provider.Background || len(p.Request.Tools) != 0 || len(k.reads) != 3 {
		t.Fatalf("preparation: %+v reads=%v", p, k.reads)
	}
	var all strings.Builder
	all.WriteString(p.Request.Instructions)
	for _, item := range p.Request.Items {
		all.Write(item)
	}
	for _, want := range []string{"persona-core", "default-voice", "user-voice", "growth", "shared-fact", "named-material", "untrusted", "exchange-"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(all.String(), "private-fact") {
		t.Fatal("personal fact leaked")
	}
	if _, err = s.Prepare(t.Context(), r.DeliveryID, "answerer", now, k); !errors.Is(err, exchange.ErrClaimed) {
		t.Fatalf("accepted turn replayed: %v", err)
	}
	if record(t, s, r.ID).State != exchange.Read {
		t.Fatal("claim not durable")
	}
}
func TestAcknowledgementNeverPrepares(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	if err := s.Acknowledge(t.Context(), r.DeliveryID, "answerer", now); err != nil {
		t.Fatal(err)
	}
	k := &knowledge{t: t}
	if _, err := s.Prepare(t.Context(), r.DeliveryID, "answerer", now, k); !errors.Is(err, exchange.ErrClosed) {
		t.Fatalf("ack preparation: %v", err)
	}
	if len(k.reads) != 0 {
		t.Fatalf("ack read knowledge: %v", k.reads)
	}
	if _, err := s.Continue(t.Context(), r.ID, exchange.Request{DeliveryID: "new", From: "asker", To: "answerer", HandOff: request().HandOff}, now); !errors.Is(err, exchange.ErrClosed) {
		t.Fatalf("ack reopened: %v", err)
	}
}

type response struct{ sent bool }

func (r *response) Next(context.Context) (provider.Event, error) {
	if r.sent {
		return provider.Event{}, io.EOF
	}
	r.sent = true
	return provider.Event{Kind: provider.Terminal}, nil
}
func (*response) Meta() provider.Meta { return provider.Meta{} }
func (*response) Close() error        { return nil }
func TestPreparedExchangeRunsThroughModelBoundary(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	prepared, err := s.Prepare(t.Context(), r.DeliveryID, "answerer", now, &knowledge{t: t})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = loop.Run(t.Context(), loop.Config{Request: prepared.Request, Stream: func(_ context.Context, req provider.Request) (provider.Stream, error) {
		calls++
		if req.Purpose != provider.Background || len(req.Tools) != 0 {
			t.Fatalf("wrong model scope: %+v", req)
		}
		for _, raw := range req.Items {
			var item struct{ Role, Content string }
			if err := json.Unmarshal(raw, &item); err != nil {
				t.Fatal(err)
			}
			if item.Role == "user" && !strings.Contains(item.Content, "<external_evidence ") {
				t.Fatalf("unwrapped exchange input: %s", raw)
			}
		}
		return &response{}, nil
	}})
	if err != nil || calls != 1 {
		t.Fatalf("model: calls=%d err=%v", calls, err)
	}
}
