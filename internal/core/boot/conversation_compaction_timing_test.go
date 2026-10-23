// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

// cacheLifetimeAdapter declares the cache lifetime a route offers; nothing is
// streamed, so only the timing rule is under test.
type cacheLifetimeAdapter struct {
	turnProvider
	lifetime provider.CapabilitySet
}

func (a *cacheLifetimeAdapter) Capabilities(provider.ModelRef) provider.CapabilitySet {
	return a.lifetime
}

func (a *cacheLifetimeAdapter) Stream(context.Context, lease.Lease, provider.Request) (provider.Stream, error) {
	return nil, &provider.Failure{Kind: provider.Unavailable}
}

func TestCacheColdReadsTheLastRequestAndTheRouteLifetime(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	now := time.Now()
	if _, recorded, err := r.lastRequestTime(t.Context()); err != nil || recorded {
		t.Fatalf("no request yet: recorded=%v err=%v", recorded, err)
	}
	recordRequest := func(id string, at time.Time) {
		_, err := r.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: id, ConversationID: r.conversation, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: "request_assembly", OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: `{}`})
		if err != nil {
			t.Fatal(err)
		}
	}
	recordRequest("request:first", now.Add(-3*time.Hour))
	recordRequest("request:second", now.Add(-2*time.Hour))
	if last, recorded, err := r.lastRequestTime(t.Context()); err != nil || !recorded || !last.Equal(now.Add(-2*time.Hour).Truncate(time.Nanosecond)) {
		t.Fatalf("last request: %v recorded=%v err=%v", last, recorded, err)
	}
	// The Responses route declares no cache lifetime and admits none, so the
	// expiry is unknown and a turn boundary never compacts early.
	r.route = &modelpath.ResponsesConfig{Model: "fixture-model", Route: "chatgpt", Adapter: &cacheLifetimeAdapter{}}
	if cold, err := r.cacheCold(t.Context(), r.route.Model, now); err != nil || cold {
		t.Fatalf("an unknown lifetime compacted early: cold=%v err=%v", cold, err)
	}
}

func TestSummaryRequestKeepsTheConversationPrefixAndDropsTheTail(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	var err error
	r.route, err = modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	queuedContextEvent(t, r, "old", "archive-marker "+strings.Repeat("old ", 2500), guard.EntryOwner, "")
	queuedContextEvent(t, r, "reply", strings.Repeat("public ", 1800), guard.EntryAgent, "")
	current := queuedContextEvent(t, r, "current", "Continue", guard.EntryOwner, "")
	var summary provider.Request
	stream := func(_ context.Context, request provider.Request) (provider.Stream, error) {
		summary = request
		text, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "archive", Actions: "read", Values: "42", Omitted: "repeated text"})
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": string(text)}}})
		return &handoffRuntimeStream{events: []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}, nil
	}
	items, _, _, events, err := r.modelContext(t.Context(), current.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := assemble.Input{MainID: "main-test", ConversationID: r.conversation, AgentID: agent, PersonaRevision: "fixture", Model: "fixture-model", Core: "core", Current: items[len(items)-1], History: items[:len(items)-1], MaxTokens: 22000}
	request, report, err := r.compactConversation(t.Context(), in, events, stream)
	if err != nil || report.Failure != "" {
		t.Fatalf("compaction failed: %+v %v", report, err)
	}
	if summary.Purpose != provider.Auxiliary || summary.Bridge != nil {
		t.Fatalf("summary acquired tool execution: %+v", summary)
	}
	if summary.Instructions != request.Instructions || len(summary.Tools) != len(request.Tools) || summary.Fields != nil && len(request.Fields) == 0 {
		t.Fatal("summary changed the instructions or tool declarations")
	}
	if summary.Layout == nil || summary.Layout.Fixed != request.Layout.Fixed || request.Layout.Fixed.Items <= 1 {
		t.Fatalf("summary lost the fixed prefix layout: %+v want %+v", summary.Layout, request.Layout)
	}
	for i, item := range request.Items[:request.Layout.Fixed.Items] {
		if !bytes.Equal(summary.Items[i], item) {
			t.Fatalf("summary changed fixed prefix item %d", i)
		}
	}
	// The segment ends where the history ends; the instruction follows it, and
	// neither the current input nor the packet is in the request.
	last := summary.Items[len(summary.Items)-1]
	if !strings.Contains(string(last), "unanswered_requests") || !bytes.Equal(summary.Items[0], request.Items[0]) {
		t.Fatalf("summary request order: %s", summary.Items)
	}
	if summary.Layout.CurrentInput.Items != len(summary.Items)-1 {
		t.Fatalf("summary marked the instruction as input: %+v", summary.Layout)
	}
	raw, _ := json.Marshal(summary.Items)
	if strings.Contains(string(raw), "Continue") || strings.Contains(string(raw), `"public-context"`) {
		t.Fatalf("summary carried the current input or packet: %s", raw)
	}
}
