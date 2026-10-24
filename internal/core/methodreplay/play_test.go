// SPDX-License-Identifier: Apache-2.0
package methodreplay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

type fakeAdapter struct {
	requests []provider.Request
	reply    string
	events   []provider.Event
}

func (a *fakeAdapter) Provider() provider.ProviderID { return provider.ChatGPT }
func (a *fakeAdapter) Class() provider.Class         { return provider.ModelAPI }
func (a *fakeAdapter) Capabilities(provider.ModelRef) provider.CapabilitySet {
	return nil
}
func (a *fakeAdapter) Models(context.Context, lease.Lease, provider.Credential) (provider.ModelList, error) {
	return provider.ModelList{}, nil
}
func (a *fakeAdapter) Quota(context.Context, provider.Credential) ([]provider.QuotaRow, error) {
	return nil, nil
}
func (a *fakeAdapter) Stream(_ context.Context, _ lease.Lease, r provider.Request) (provider.Stream, error) {
	a.requests = append(a.requests, r)
	if a.events != nil {
		return &fakeStream{events: a.events}, nil
	}
	item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant",
		"content": []map[string]string{{"type": "output_text", "text": a.reply}}})
	return &fakeStream{events: []provider.Event{
		{Kind: provider.Delta, Index: 0, Delta: a.reply},
		{Kind: provider.ItemDone, Index: 0, Item: item},
		{Kind: provider.Terminal},
	}}, nil
}

type fakeStream struct {
	events []provider.Event
}

func (s *fakeStream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}
func (s *fakeStream) Meta() provider.Meta { return provider.Meta{} }
func (s *fakeStream) Close() error        { return nil }

func tailoredVariant(role, core, record string) Variant {
	return Variant{Name: "tailored", Presets: map[string]Preset{
		role: {Layers: agents.Layers{Core: core, DefaultVoice: "voice"}, WorkMethod: record},
	}}
}

func TestPlayerSendsLayersAndDropsInjectedTurns(t *testing.T) {
	adapter := &fakeAdapter{reply: "확인했습니다."}
	c := Case{Version: 1, ID: "dev-001", Role: "dev", ReplyType: "status", Kind: "accepted",
		Checks: []Check{Length}, Budget: 50, Turns: []Turn{
			{Role: "user", Text: "압축 요약입니다", Injected: true},
			{Role: "user", Text: "상태 알려줘"},
		}}
	player := Player{Adapter: adapter, Model: "fixture"}
	got, err := player.Play(t.Context(), c, tailoredVariant("dev", "core text", `{"schema":1,"preset":"dev"}`))
	if err != nil || got != "확인했습니다." {
		t.Fatalf("reply=%q error=%v", got, err)
	}
	if len(adapter.requests) != 1 {
		t.Fatalf("requests=%d", len(adapter.requests))
	}
	request := adapter.requests[0]
	if request.Purpose != provider.Replay {
		t.Fatalf("purpose=%s", request.Purpose)
	}
	if len(request.Items) != 1 {
		t.Fatalf("injected turn reached the model: %s", request.Items)
	}
	for _, want := range []string{"core text", `<work_method>`, `"preset":"dev"`} {
		if !strings.Contains(request.Instructions, want) {
			t.Fatalf("instructions lack %q: %s", want, request.Instructions)
		}
	}
}

func TestPlayerRefusesAnUnknownRole(t *testing.T) {
	player := Player{Adapter: &fakeAdapter{}, Model: "fixture"}
	c := Case{ID: "dev-001", Role: "dev", Turns: []Turn{{Role: "user", Text: "상태"}}}
	if _, err := player.Play(t.Context(), c, Variant{Name: "tailored"}); !errors.Is(err, ErrConfig) {
		t.Fatalf("error=%v", err)
	}
}

func TestPlayerReportsAnEmptyReply(t *testing.T) {
	player := Player{Adapter: &fakeAdapter{events: []provider.Event{{Kind: provider.Terminal}}}, Model: "fixture"}
	c := Case{ID: "dev-001", Role: "dev", Turns: []Turn{{Role: "user", Text: "상태"}}}
	if _, err := player.Play(t.Context(), c, tailoredVariant("dev", "core", `{"schema":1,"preset":"dev"}`)); !errors.Is(err, ErrReply) {
		t.Fatalf("error=%v", err)
	}
}

func TestPlayerRejectsAnUnconfiguredRoute(t *testing.T) {
	if _, err := (Player{}).Play(t.Context(), Case{}, Variant{}); !errors.Is(err, ErrConfig) {
		t.Fatalf("error=%v", err)
	}
}
