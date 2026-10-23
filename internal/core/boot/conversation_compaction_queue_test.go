// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestConversationCompactionAccountsForQueuedInputs(t *testing.T) {
	for _, scenario := range []string{"queued", "late-reply", "retracted-queue"} {
		t.Run(scenario, func(t *testing.T) {
			r, _, agent, _ := managementFixture(t)
			r.agent, r.conversation = agent, "main:"+agent
			var err error
			r.route, err = modelpath.ResponsesFromEnv(func(key string) string {
				return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
			})
			if err != nil {
				t.Fatal(err)
			}
			queuedContextEvent(t, r, "old", strings.Repeat("old ", 2500), guard.EntryOwner, "")
			queuedContextEvent(t, r, "reply", strings.Repeat("public ", 1800), guard.EntryAgent, "")
			if scenario == "late-reply" {
				queuedContextEvent(t, r, "recent", "Recent prior input", guard.EntryOwner, "")
			}
			current := queuedContextEvent(t, r, "current", "Current input", guard.EntryOwner, "")
			queuedContextEvent(t, r, "queued", "Future queued input", guard.EntryOwner, "")
			queuedContextEvent(t, r, "next", "Next queued input", guard.EntryOwner, "")
			if scenario == "late-reply" {
				queuedContextEvent(t, r, "late", "Late prior reply", guard.EntryAgent, "")
			}
			if scenario == "retracted-queue" {
				forgetQueuedSource(t, r, "queued")
			}
			calls := 0
			stream := func(_ context.Context, request provider.Request) (provider.Stream, error) {
				calls++
				raw, _ := json.Marshal(request.Items)
				for _, excluded := range []string{"Current input", "Future queued input", "Next queued input"} {
					if strings.Contains(string(raw), excluded) {
						t.Errorf("summary contains protected input %q", excluded)
					}
				}
				text, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "archive", Actions: "read", Values: "archive", Omitted: "repeated text"})
				item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": string(text)}}})
				return &handoffRuntimeStream{events: []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}, nil
			}
			fit := func(id string) provider.Request {
				t.Helper()
				items, _, _, events, err := r.modelContext(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				in := assemble.Input{AgentID: agent, PersonaRevision: "fixture", Model: "fixture-model", Core: "core", Current: items[len(items)-1], History: items[:len(items)-1], MaxTokens: 22000}
				request, report, err := r.compactConversation(t.Context(), in, events, stream)
				if err != nil || report.Failure != "" {
					t.Fatalf("queued input blocked compaction: calls=%d err=%v failure=%s", calls, err, report.Failure)
				}
				return request
			}
			request := fit(current.ID)
			raw, _ := json.Marshal(request.Items)
			if calls != 1 || strings.Contains(string(raw), "queued input") || strings.Count(string(raw), "Current input") != 1 {
				t.Fatalf("current request lost isolation: calls=%d request=%s", calls, raw)
			}
			store := compaction.New(r.db)
			frontier, err := store.Frontier(t.Context(), r.conversation)
			if err != nil {
				t.Fatal(err)
			}
			covered, _, err := store.ExpandEvents(t.Context(), agent, frontier.Root, 0, 100)
			if err != nil || !reflect.DeepEqual(covered, []string{"old", "reply"}) {
				t.Fatalf("summary coverage includes queued/protected inputs: %v %v", covered, err)
			}
			if scenario != "retracted-queue" {
				request = fit("queued")
				raw, _ = json.Marshal(request.Items)
				if strings.Count(string(raw), "Future queued input") != 1 || strings.Contains(string(raw), "Next queued input") || calls != 1 {
					t.Fatalf("queued turn lost its input or summary reuse: calls=%d request=%s", calls, raw)
				}
			}
		})
	}
}
