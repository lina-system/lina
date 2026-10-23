// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/compaction"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/memory"
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

func TestConversationCompactionPreservesStandingProfile(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(map[bool]string{false: "below-threshold", true: "summarized"}[large], func(t *testing.T) {
			r, _, agent, _ := managementFixture(t)
			r.agent, r.conversation = agent, "profile-source"
			source := queuedContextEvent(t, r, "profile-source", "I prefer concise answers.", guard.EntryOwner, "")
			_, err := coreexec.NewMemory(r.db, nil, nil).Remember(t.Context(), memory.Write{Person: r.person, Agent: agent, Item: memory.Item{
				Kind: memory.Preference, Content: "I prefer concise answers.", Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: source.ID, Precision: memory.Exact}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			r.conversation = "main:" + agent
			r.route, err = modelpath.ResponsesFromEnv(func(key string) string {
				return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
			})
			if err != nil {
				t.Fatal(err)
			}
			if large {
				queuedContextEvent(t, r, "old", strings.Repeat("old ", 2500), guard.EntryOwner, "")
				queuedContextEvent(t, r, "reply", strings.Repeat("public ", 1800), guard.EntryAgent, "")
			}
			current := queuedContextEvent(t, r, "current", "Continue", guard.EntryOwner, "")
			items, refs, _, events, err := r.modelContext(t.Context(), current.ID)
			if err != nil || len(refs) != 1 {
				t.Fatalf("profile setup failed: refs=%v err=%v", refs, err)
			}
			profile := items[0]
			calls := 0
			stream := func(_ context.Context, request provider.Request) (provider.Stream, error) {
				calls++
				for _, item := range request.Items {
					if bytes.Equal(item, profile) {
						t.Fatal("standing profile entered summary generation")
					}
				}
				text, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "archive", Actions: "read", Values: "archive", Omitted: "repeated text"})
				item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": string(text)}}})
				return &handoffRuntimeStream{events: []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}, nil
			}
			in := assemble.Input{AgentID: agent, PersonaRevision: "fixture", Model: "fixture-model", Core: "core", Current: items[len(items)-1], History: items[:len(items)-1], MaxTokens: 22000}
			request, report, err := r.compactConversation(t.Context(), in, events, stream)
			if err != nil || report.Failure != "" {
				t.Fatalf("compaction: %v %+v", err, report)
			}
			count := 0
			for _, item := range request.Items {
				if bytes.Equal(item, profile) {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("standing-profile projection occurs %d times, want 1", count)
			}
			if large != (calls == 1) {
				t.Fatalf("summary calls=%d for large=%v", calls, large)
			}
		})
	}
}

func TestConversationCompactionTrimsOptionalContext(t *testing.T) {
	for _, scenario := range []string{"no-history", "short-history", "large-history", "mandatory-near-limit", "mandatory-too-large"} {
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
			limit, currentText := 3000, "Continue"
			if scenario == "short-history" {
				queuedContextEvent(t, r, "old", "Earlier input", guard.EntryOwner, "")
			}
			if scenario == "large-history" {
				limit = 22000
				queuedContextEvent(t, r, "old", strings.Repeat("old ", 2500), guard.EntryOwner, "")
				queuedContextEvent(t, r, "reply", strings.Repeat("public ", 1800), guard.EntryAgent, "")
			}
			if scenario == "mandatory-near-limit" {
				currentText = strings.Repeat("x", 2550)
			}
			if scenario == "mandatory-too-large" {
				currentText = strings.Repeat("x", 4000)
			}
			current := queuedContextEvent(t, r, "current", currentText, guard.EntryOwner, "")
			items, _, _, events, err := r.modelContext(t.Context(), current.ID)
			if err != nil {
				t.Fatal(err)
			}
			packet := assemble.ContextPacket{
				{ID: "clock", Kind: assemble.Clock, Text: "2026-10-23T00:00:00Z"},
				{ID: "optional", Kind: assemble.Memory, Text: strings.Repeat("optional relationship ", 2000)},
			}
			in := assemble.Input{AgentID: agent, PersonaRevision: "fixture", Model: "fixture-model", Core: "core", Current: items[len(items)-1], History: items[:len(items)-1], Packet: packet, MaxTokens: limit}
			calls := 0
			stream := func(_ context.Context, request provider.Request) (provider.Stream, error) {
				calls++
				raw, _ := json.Marshal(request.Items)
				if bytes.Contains(raw, []byte("optional relationship")) {
					t.Fatal("optional memory entered summary generation")
				}
				text, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "archive", Actions: "read", Values: "archive", Omitted: "repeated text"})
				item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": string(text)}}})
				return &handoffRuntimeStream{events: []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}, nil
			}
			request, report, err := r.compactConversation(t.Context(), in, events, stream)
			if scenario == "mandatory-too-large" {
				if len(request.Items) != 0 || (err == nil && report.Failure != assemble.TooLarge) {
					t.Fatal("mandatory input escaped hard limit")
				}
				return
			}
			if err != nil || report.Failure != "" {
				t.Fatalf("optional context blocked a fitting request: err=%v failure=%s", err, report.Failure)
			}
			raw, _ := json.Marshal(request.Items)
			if conversationRequestSize(request) > limit || bytes.Contains(raw, []byte("optional relationship")) || !bytes.Contains(raw, []byte("2026-10-23T00:00:00Z")) {
				t.Fatal("context selection lost its budget or mandatory clock")
			}
			dropped := false
			for _, item := range report.Dropped {
				if item.ID == "optional" && item.Reason == assemble.Budget {
					dropped = true
				}
			}
			if !dropped {
				t.Fatal("optional-budget drop missing from report")
			}
			if scenario == "large-history" {
				if calls != 1 {
					t.Fatalf("large history summary calls=%d", calls)
				}
			} else {
				baseline, baselineReport := assemble.Assemble(in, conversationRequestSize)
				if baselineReport.Failure != "" || !reflect.DeepEqual(request, baseline) || calls != 0 {
					t.Fatalf("compaction changed the previously fitting request: calls=%d", calls)
				}
			}
		})
	}
}
