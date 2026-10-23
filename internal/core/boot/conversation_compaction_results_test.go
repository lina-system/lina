// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/provider"
)

// Summary generation reloads each source on its own. A completed presentation
// is bounded only when that reload is paired with its receipt again, including
// when a stale root is rebuilt. A refused presentation keeps its arguments.
func TestConversationCompactionSummarizesBoundedResultCalls(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	var err error
	r.route, err = modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	queuedContextEvent(t, r, "old", "private-marker "+strings.Repeat("old ", 2500), guard.EntryOwner, "")
	admitResultCall(t, r, "stored", "RESULT-BODY-SECRET", "ok")
	admitResultCall(t, r, "refused", "REFUSED-BODY-SECRET", "denied")
	queuedContextEvent(t, r, "reply", strings.Repeat("public ", 1800), guard.EntryAgent, "")
	current := queuedContextEvent(t, r, "current", "Continue", guard.EntryOwner, "")
	summary := func() provider.Stream {
		text, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "results", Actions: "presented", Values: "none", Omitted: "bodies"})
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": string(text)}}})
		return &handoffRuntimeStream{events: []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}
	}
	check := func(request provider.Request) {
		t.Helper()
		raw, _ := json.Marshal(request.Items)
		text := string(raw)
		if strings.Contains(text, "RESULT-BODY-SECRET") || !strings.Contains(text, "result body stored") {
			t.Fatalf("summary sent a completed result body: %s", raw)
		}
		if !strings.Contains(text, "REFUSED-BODY-SECRET") {
			t.Fatalf("summary dropped a refused presentation: %s", raw)
		}
	}
	calls := 0
	stream := func(_ context.Context, request provider.Request) (provider.Stream, error) {
		calls++
		check(request)
		return summary(), nil
	}
	fit := func() {
		t.Helper()
		items, _, _, events, err := r.modelContext(t.Context(), current.ID)
		if err != nil {
			t.Fatal(err)
		}
		in := assemble.Input{MainID: "main-test", ConversationID: r.conversation, AgentID: agent, PersonaRevision: "fixture", Model: "fixture-model", Core: "core", Current: items[len(items)-1], History: items[:len(items)-1], MaxTokens: 22000}
		request, report, err := r.compactConversation(t.Context(), in, events, stream)
		if err != nil || report.Failure != "" {
			t.Fatalf("compaction: %v %+v", err, report)
		}
		if conversationRequestSize(request) >= 22000 {
			t.Fatalf("history never fit: size=%d", conversationRequestSize(request))
		}
	}
	fit()
	if calls != 1 {
		t.Fatalf("summary calls=%d, want 1", calls)
	}
	forgetQueuedSource(t, r, "old")
	fit()
	if calls != 2 {
		t.Fatalf("rebuild calls=%d, want 2", calls)
	}
}

func admitResultCall(t *testing.T, r *conversationRuntime, id, secret, code string) {
	t.Helper()
	arguments := fmt.Sprintf(`{"type":"document_draft","title":"Draft","summary":"Ready","body":{"blocks":[{"kind":"paragraph","text":%q}]}}`, secret)
	call := fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":"present_result","arguments":%s}`, id, strconv.Quote(arguments))
	output := fmt.Sprintf(`{"type":"function_call_output","call_id":%q,"output":%s}`, id, strconv.Quote(fmt.Sprintf(`{"id":%q,"code":%q}`, id, code)))
	batch := []ledger.Admission{
		{Entry: guard.EntryAgent, Input: ledger.Input{
			ID: id + "-call", ConversationID: r.conversation, AgentID: r.agent, ParentID: "old",
			Surface: ledger.Surface{Kind: "tui"}, Locator: "tool:present_result:" + id,
			ScaffoldKind: "provider_item", Body: call,
			OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0), TimeQuality: "observed",
		}},
		{Entry: guard.EntryTool, Input: ledger.Input{
			ID: id + "-output", ConversationID: r.conversation, AgentID: r.agent, ParentID: "old",
			Surface: ledger.Surface{Kind: "tui"}, Locator: "tool:present_result:" + id + ":output",
			ScaffoldKind: "provider_item", Body: output,
			OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0), TimeQuality: "observed",
		}},
	}
	if _, err := r.journal().AdmitBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
}
