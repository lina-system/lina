// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/recall"
)

func TestM3MessengerRecallInstalledCore(t *testing.T) {
	const fact = "도서관 library meeting at noon"
	var seeded atomic.Bool
	var completed atomic.Int32
	var anchor string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var last struct {
			Type, Output string
			Call         string `json:"call_id"`
		}
		if len(req.Input) > 0 {
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &last)
		}
		if last.Type == "function_call_output" {
			if !strings.Contains(last.Output, `"code":"ok"`) {
				t.Errorf("tool failed: %s", last.Output)
				emitResponse(w, "Read failed.", true)
				return
			}
			var envelope struct {
				Output json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal([]byte(last.Output), &envelope); err != nil {
				t.Error(err)
			}
			switch last.Call {
			case "chats":
				if !strings.Contains(last.Output, "Study group") {
					t.Error("chat omitted")
				}
				emitPersonaCall(w, "korean", "recall", map[string]any{"query": "도서관", "origin": "messenger", "platform": "discord", "account": "synthetic-account", "chat": "study", "person_only": true})
			case "korean":
				var result struct {
					Results []recall.MessengerPage `json:"results"`
				}
				if err := json.Unmarshal(envelope.Output, &result); err != nil {
					t.Error(err)
				}
				if len(result.Results) != 1 || len(result.Results[0].Items) != 1 {
					t.Errorf("messenger result: %s", last.Output)
					emitResponse(w, "No evidence.", true)
					return
				}
				item := result.Results[0].Items[0]
				anchor = item.ID
				if !item.Author.Person || item.Author.Name != "Owner" || item.Revision != 1 || !strings.Contains(string(item.LegacyReference), "record-1") {
					t.Errorf("missing provenance: %+v", item)
				}
				emitPersonaCall(w, "english", "recall", map[string]any{"query": "library", "origin": "messenger", "author": "friend"})
			case "english":
				if !strings.Contains(last.Output, "record-2") || strings.Contains(last.Output, "record-1") {
					t.Errorf("author filter: %s", last.Output)
				}
				emitPersonaCall(w, "context", "expand", map[string]any{"id": "messenger:" + anchor, "limit": 10})
			case "context":
				if !strings.Contains(last.Output, fact) || !strings.Contains(last.Output, "Bring the library card") {
					t.Errorf("context: %s", last.Output)
				}
				completed.Add(1)
				emitResponse(w, "The meeting is at noon.", true)
			default:
				t.Errorf("unexpected tool call %s", last.Call)
				emitResponse(w, "Unexpected read.", true)
			}
			return
		}
		search := false
		for _, raw := range req.Input {
			var item struct{ Content string }
			_ = json.Unmarshal(raw, &item)
			if item.Content == "Search my messenger study chat" {
				search = true
			}
			if seeded.Load() && strings.Contains(item.Content, fact) {
				t.Error("messenger record entered the request without an explicit tool result")
			}
		}
		if !search {
			emitResponse(w, "Ready.", true)
			return
		}
		names := map[string]bool{}
		for _, raw := range req.Tools {
			var d struct{ Name string }
			_ = json.Unmarshal(raw, &d)
			names[d.Name] = true
		}
		if !names["messenger_chats"] || !names["recall"] || !names["expand"] {
			t.Error("messenger tools are not declared")
		}
		emitPersonaCall(w, "chats", "messenger_chats", map[string]any{"platform": "discord"})
	}))
	defer server.Close()
	h, _ := installedConversationHarnessWithin(t, 3*time.Minute, server.URL)
	start := func() *process {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "PATH="+os.Getenv("PATH"), "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		core.start(t)
		h.connectWithin(core, 30*time.Second).Close()
		waitCoreReady(t, h)
		return core
	}
	core := start()
	client, conversation := conversationClient(t, h)
	agent := client.Welcome.Agents[0].AgentID
	admittedTurn(t, h, client, conversation, "identity", "Ready")
	client.Close()
	h.stopCore(core)
	db, closeStore := installedAcceptanceStore(t, h)
	var person string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT person_id FROM conv_event WHERE agent_id=? AND speaker_kind='person' LIMIT 1", agent).Scan(&person)
	}); err != nil {
		t.Fatal(err)
	}
	store := messenger.Open(db, nil, nil)
	source := messenger.Source{Person: person, Platform: "discord", Account: "synthetic-account", Mode: messenger.ModeOfficial, Adapter: "fixture", State: messenger.StateImportOnly}
	if err := store.Connect(h.ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertChats(h.ctx, source, []messenger.Chat{{ID: "study", Name: "Study group", Kind: "group"}}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	if _, err := store.AdmitMessages(h.ctx, source, "study", []messenger.Message{{ID: "record-1", Author: messenger.Author{ID: "owner", Name: "Owner"}, Route: messenger.RoutePerson, At: at, Text: fact}, {ID: "record-2", Author: messenger.Author{ID: "friend", Name: "Friend"}, Route: messenger.RouteParticipant, At: at.Add(time.Minute), Text: "Bring the library card"}}); err != nil {
		t.Fatal(err)
	}
	closeStore()
	for _, action := range []string{"--enable", "--disable", "--enable"} {
		command := h.command("embedding", "messenger", "--state-root", h.root, "--person", person, "--platform", "discord", "--account", "synthetic-account", action)
		command.start(t)
		command.wait(t, 0)
	}
	seeded.Store(true)
	core = start()
	waitForRows(t, h, nil, 1, "SELECT count(*) FROM messenger_window")
	client, conversation = conversationClient(t, h)
	admittedTurn(t, h, client, conversation, "ordinary", "Tell me about library schedules")
	admittedTurn(t, h, client, conversation, "search", "Search my messenger study chat")
	client.Close()
	h.stopCore(core)
	if completed.Load() != 1 {
		t.Fatalf("completed messenger reads=%d", completed.Load())
	}
}
