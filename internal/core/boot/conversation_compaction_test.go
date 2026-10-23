// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestConversationCompactionFailureRecoveryAndRetraction(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	var err error
	r.route, err = modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	queuedContextEvent(t, r, "old", "private-marker-42 "+strings.Repeat("old ", 2500), guard.EntryOwner, "")
	queuedContextEvent(t, r, "reply", strings.Repeat("public ", 1800), guard.EntryAgent, "")
	current := queuedContextEvent(t, r, "current", "Continue", guard.EntryOwner, "")
	calls := 0
	failed := errors.New("summary unavailable")
	refuse := true
	retracted := false
	stream := func(_ context.Context, request provider.Request) (provider.Stream, error) {
		calls++
		if refuse {
			return nil, failed
		}
		raw, _ := json.Marshal(request.Items)
		if strings.Contains(string(raw), "private-marker-42") == retracted {
			t.Fatal("summary source did not honor retraction")
		}
		values := "42"
		if retracted {
			values = "public content"
		}
		text, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "archive", Actions: "read", Values: values, Omitted: "repeated text"})
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": string(text)}}})
		return &handoffRuntimeStream{events: []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}, nil
	}
	fit := func() (provider.Request, error) {
		items, _, _, events, err := r.modelContext(t.Context(), current.ID)
		if err != nil {
			return provider.Request{}, err
		}
		in := assemble.Input{AgentID: agent, PersonaRevision: "fixture", Model: "fixture-model", Core: "core", Current: items[len(items)-1], History: items[:len(items)-1], MaxTokens: 22000}
		request, report, err := r.compactConversation(t.Context(), in, events, stream)
		if err == nil && report.Failure != "" {
			t.Fatalf("fit failed: %+v", report)
		}
		return request, err
	}
	if _, err := fit(); !errors.Is(err, failed) {
		t.Fatalf("summary failure was hidden: %v", err)
	}
	frontier, err := compaction.New(r.db).Frontier(t.Context(), r.conversation)
	if err != nil || frontier.Root != "" {
		t.Fatalf("failed summary published: %+v %v", frontier, err)
	}
	refuse = false
	request, err := fit()
	if err != nil {
		t.Fatal(err)
	}
	if conversationRequestSize(request) >= 22000 || calls != 2 {
		t.Fatalf("history never fit: size=%d calls=%d", conversationRequestSize(request), calls)
	}
	if _, err := fit(); err != nil || calls != 2 {
		t.Fatalf("published summary was not reused: calls=%d err=%v", calls, err)
	}
	retracted = true
	forgetQueuedSource(t, r, "old")
	request, err = fit()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request.Items)
	if calls != 3 || strings.Contains(string(raw), "private-marker-42") || !strings.Contains(string(raw), "public content") {
		t.Fatalf("stale summary reused after retraction: calls=%d request=%s", calls, raw)
	}
}
