// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestConversationCompactionRebuildCarriesOnlyVisibleValues(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	var err error
	r.route, err = modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	var records []compaction.Record
	for i := range 3 {
		id := fmt.Sprintf("source-%d", i)
		queuedContextEvent(t, r, id, id, guard.EntryOwner, "")
		records = append(records, compaction.Record{ID: id})
	}
	current := queuedContextEvent(t, r, "current", "Continue", guard.EntryOwner, "")
	n := 0
	b := compaction.Builder{Store: compaction.New(r.db), Events: r.journal(), Agent: agent, Conversation: r.conversation, Model: "fixture-model", Now: time.Now, NextID: func() string { n++; return fmt.Sprintf("summary-%d", n) }, Generate: func(_ context.Context, s compaction.PassSegment) (compaction.Text, error) {
		return compaction.Text{Requests: "none", Topics: "receipts", Actions: "none", Values: "retired-42\nsurvivor-84\nnew-126", Omitted: "wording"}, nil
	}}
	var root compaction.Published
	for i := range records {
		segment := compaction.PassSegment{RootID: root.ID, Root: root.Item, Records: records[i : i+1], Verbatim: records[i+1:], Protected: []compaction.Record{{ID: current.ID}}}
		text, err := b.Summarize(t.Context(), segment)
		if err != nil {
			t.Fatal(err)
		}
		root, err = b.Publish(t.Context(), segment, text)
		if err != nil {
			t.Fatal(err)
		}
	}
	forgetQueuedSource(t, r, "source-0")
	calls := 0
	stream := func(_ context.Context, request provider.Request) (provider.Stream, error) {
		calls++
		values := "none"
		for _, raw := range request.Items {
			var item struct{ Content string }
			_ = json.Unmarshal(raw, &item)
			if strings.Contains(item.Content, "retired-42") || item.Content == "source-0" {
				t.Fatal("invalidated carried values reached the model")
			}
			// The provider extracts from raw events but forgets child summaries.
			if item.Content == "source-1" {
				values = "survivor-84"
			}
			if item.Content == "source-2" {
				values = "new-126"
			}
		}
		text, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "receipts", Actions: "none", Values: values, Omitted: "wording"})
		item, _ := json.Marshal(map[string]any{"role": "assistant", "content": string(text)})
		return &handoffRuntimeStream{events: []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}, nil
	}
	items, _, _, events, err := r.modelContext(t.Context(), current.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := assemble.Input{AgentID: agent, PersonaRevision: "fixture", Model: "fixture-model", Core: "core", Current: items[len(items)-1], History: items[:len(items)-1], MaxTokens: 22000}
	request, report, err := r.compactConversation(t.Context(), in, events, stream)
	if err != nil || report.Failure != "" {
		t.Fatalf("rebuild failed: %v %+v", err, report)
	}
	raw, _ := json.Marshal(request.Items)
	if calls != 3 || strings.Contains(string(raw), "retired-42") || !strings.Contains(string(raw), "survivor-84") || !strings.Contains(string(raw), "new-126") {
		t.Fatalf("rebuild lost surviving values or restored a removed value: calls=%d request=%s", calls, raw)
	}
}
