// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

type conversationWebStub struct{ calls atomic.Int32 }

func (p *conversationWebStub) Search(context.Context, string, int) ([]coreexec.WebHit, error) {
	p.calls.Add(1)
	return []coreexec.WebHit{{URL: "https://example.com/x", Title: "X", Snippet: "Latest X"}}, nil
}
func (p *conversationWebStub) Extract(context.Context, string) (string, error) {
	p.calls.Add(1)
	return "Page", nil
}

func TestConversationWebWaitsForNetworkApproval(t *testing.T) {
	for _, choice := range []protocol.ApprovalChoice{protocol.ApprovalChoiceOnce, protocol.ApprovalChoiceAlwaysAllow, protocol.ApprovalChoiceDeny} {
		t.Run(string(choice), func(t *testing.T) {
			r := controlRuntime(t)
			p := &conversationWebStub{}
			bindings := map[string]loop.Tool{}
			decl, err := r.bindWebTools("web-turn", p, bindings)
			if err != nil || len(decl) != 2 {
				t.Fatal(decl, err)
			}
			session := clientsrv.Session{ConnectionID: "web-client", PersonID: r.person, Epoch: r.epoch, CanDecide: true, Capabilities: []protocol.Capability{clientsrv.DecisionsCapability}}
			r.identity.DecisionPresence(session, true)
			defer r.identity.DecisionPresence(session, false)
			_, sub, err := r.topics.Subscribe("waiting", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan loop.Result, 1)
			go func() {
				done <- bindings["web_search"].Execute(ctx, provider.ToolCall{ID: "call", Name: "web_search", Arguments: `{"query":"latest X"}`})
			}()
			frame, err := sub.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var request protocol.ApprovalRequest
			if err := json.Unmarshal(frame.Event.Data, &request); err != nil || request.Card == nil {
				t.Fatal(err)
			}
			if p.calls.Load() != 0 {
				t.Fatal("provider called before network grant")
			}
			_, err = r.identity.Operations[protocol.KindApprovalAnswer].Handle(ctx, session, protocol.Envelope{Payload: protocol.ApprovalAnswer{CardID: &request.Card.ID, Revision: &request.Card.Revision, Choice: choice}})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				want := loop.OK
				count := int32(1)
				if choice == protocol.ApprovalChoiceDeny {
					want, count = loop.Denied, 0
				}
				if result.Code != want || p.calls.Load() != count {
					t.Fatalf("result=%+v calls=%d", result, p.calls.Load())
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if choice != protocol.ApprovalChoiceDeny {
				start, stream, err := r.topics.Subscribe("running", nil)
				if err != nil {
					t.Fatal(err)
				}
				stream.Close()
				if start.Snapshot == nil || len(start.Snapshot.Items) != 1 {
					t.Fatal("missing web activity")
				}
				var activity protocol.RunningUpdate
				if json.Unmarshal(start.Snapshot.Items[0].Data, &activity) != nil || activity.Text == nil || *activity.Text != "completed: web_search" {
					t.Fatal("web activity did not settle")
				}
			}
			if choice == protocol.ApprovalChoiceAlwaysAllow {
				result := bindings["web_search"].Execute(ctx, provider.ToolCall{ID: "second", Name: "web_search", Arguments: `{"query":"another X"}`})
				if result.Code != loop.OK || p.calls.Load() != 2 {
					t.Fatal("standing network grant not honored", result)
				}
			}
			if len(r.personaGrants) != 0 {
				t.Fatal("web grant outlived call")
			}
		})
	}
}

func TestConversationWebMissingProviderOffersNoTools(t *testing.T) {
	r := controlRuntime(t)
	bindings := map[string]loop.Tool{}
	decl, err := r.bindWebTools("turn", nil, bindings)
	if err != nil || len(decl) != 0 || len(bindings) != 0 {
		t.Fatal("unconfigured web tools offered", err)
	}
}

func TestConversationWebProviderForNewAgent(t *testing.T) {
	r := controlRuntime(t)
	var calls int
	r.webTransport = channelTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != "https://api.firecrawl.dev/v2/search" {
			t.Fatalf("unexpected web endpoint: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{"web":[{"title":"Latest X","url":"https://example.com/x","description":"Current details"}]}}`))}, nil
	})
	owner := clientsrv.Session{PersonID: r.person, Epoch: r.epoch, CanDecide: true}
	key := "fc-synthetic-routed-key"
	if _, err := r.Web(t.Context(), owner, protocol.WebRequest{Operation: "set", Key: &key}); err != nil {
		t.Fatal(err)
	}
	agent, err := r.agents.Create(t.Context(), agents.Create{Name: "Web reader", PersonID: r.person, Preset: "lina"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := r.router.view(t.Context(), owner, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Refuse to leave the fixture boundary if routing lost its transport.
	if view.webTransport == nil {
		t.Fatal("new agent lost the configured web transport")
	}
	p, err := view.selectedWebProvider()
	if err != nil || p == nil {
		t.Fatalf("new agent has no configured web provider: %v", err)
	}
	hits, err := p.Search(t.Context(), "latest X", 1)
	if err != nil || len(hits) != 1 || hits[0].URL != "https://example.com/x" || calls != 1 {
		t.Fatalf("routed search: hits=%+v calls=%d err=%v", hits, calls, err)
	}
}
