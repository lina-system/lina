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
	// The Claude API-key route admits a 1-hour lifetime, so the two-hour-old
	// request above has an expired cache and a turn boundary may compact early.
	r.route = &modelpath.ResponsesConfig{Model: "fixture-model", Route: "claude_api_key", Adapter: &cacheLifetimeAdapter{lifetime: provider.CapabilitySet{provider.CacheLifetime1hCapability}}}
	if cold, err := r.cacheCold(t.Context(), r.route.Model, now); err != nil || !cold {
		t.Fatalf("an expired 1-hour cache did not compact early: cold=%v err=%v", cold, err)
	}
}

func TestSummaryRequestKeepsTheConversationPrefixAndDropsTheTail(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	// The Claude API-key route is the one that admits cache marks and a 1-hour
	// lifetime; the summary must stay a valid marked request there.
	marks := provider.CapabilitySet{provider.PrefixCacheCapability, provider.CacheIdentityCapability, provider.CacheMarksCapability, provider.CacheLifetime1hCapability}
	r.route = &modelpath.ResponsesConfig{Model: "fixture-model", Route: "claude_api_key", Adapter: &cacheLifetimeAdapter{lifetime: marks}}
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
	// A route that admits marks must accept the trimmed request Core sends.
	if summary.CacheIdentity == "" || summary.CacheIdentity != request.CacheIdentity {
		t.Fatalf("summary lost the conversation cache identity: %q want %q", summary.CacheIdentity, request.CacheIdentity)
	}
	if len(summary.Capabilities.CacheMarks) == 0 {
		t.Fatal("summary carried no cache mark on a marks route")
	}
	for _, mark := range summary.Capabilities.CacheMarks {
		if mark.Position != summary.Layout.Fixed && (summary.Layout.PreviousInput == nil || mark.Position != *summary.Layout.PreviousInput) {
			t.Fatalf("summary marked a boundary outside its prefix: %+v in %+v", mark, summary.Layout)
		}
	}
	supported, admitted := r.route.Capabilities(summary.Model)
	if err := provider.ValidateUse(summary, supported, admitted); err != nil {
		t.Fatalf("summary request is not valid on its route: %v", err)
	}
}
