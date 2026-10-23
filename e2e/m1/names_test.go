// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestNamesReachEveryAgentAndCountRetiredAliases(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input, Tools []json.RawMessage
			Instructions string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		if n == 1 || n == 3 {
			if !strings.Contains(string(mustJSON(t, req.Tools)), `"remember_name"`) {
				t.Error("running Core did not declare remember_name")
				emitResponse(w, "Unavailable.", true)
				return
			}
			current, quote := "Cedar", "The desk computer is Cedar."
			if n == 3 {
				current, quote = "Birch", "The desk computer is Birch, not Cedar."
			}
			emitPersonaCall(w, fmt.Sprintf("name-%d", n), "remember_name", map[string]any{"subject": "desk computer", "current": current, "quote": quote})
			return
		}
		if n == 2 || n == 4 {
			var output struct{ Output string }
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &output)
			if !strings.Contains(output.Output, `"remembered":true`) {
				t.Errorf("name tool did not commit: %s", output.Output)
			}
			emitResponse(w, "Recorded.", true)
			return
		}
		found := false
		for _, raw := range req.Input {
			var msg struct{ Role, Content string }
			if json.Unmarshal(raw, &msg) == nil && strings.Contains(msg.Content, `origin="name-memory:`) {
				msg.Content = html.UnescapeString(msg.Content)
				found = true
				if msg.Role != "user" || !strings.Contains(msg.Content, `"current":"Birch"`) || !strings.Contains(msg.Content, `"retired_aliases":["Cedar"]`) {
					t.Errorf("incorrect name packet: %s", raw)
				}
			}
		}
		if !found {
			t.Error("name missing from prepared ContextPacket")
		}
		if n == 5 {
			emitResponse(w, "Cedar is ready. Cedar is ready.", true)
		} else {
			emitResponse(w, "Birch is ready.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "agents", Version: "1.0"})
	admittedTurn(t, h, c, conversation, "name-create", "The desk computer is Cedar.")
	if calls.Load() != 2 {
		t.Fatal("name creation did not execute its tool")
	}
	admittedTurn(t, h, c, conversation, "name-correct", "The desk computer is Birch, not Cedar.")
	if calls.Load() != 4 {
		t.Fatal("name correction did not execute its tool")
	}
	person := c.Welcome.PersonID
	op := "list"
	if _, err := c.Agent(h.ctx, protocol.AgentRequest{Operation: &op}); err != nil {
		t.Fatal(err)
	}
	e, err := c.Receive(h.ctx)
	if err != nil || e.Error != nil {
		t.Fatal("agents list", err, e.Error)
	}
	list := e.Payload.(protocol.AgentResult)
	if list.Agents == nil || len(*list.Agents) != 8 {
		t.Fatal("missing starter agents")
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	var breached string
	for i, agent := range *list.Agents {
		c, _ := conversationClient(t, h)
		conversation = "main:" + agent.AgentID
		topic := "conversation:" + conversation
		if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Receive(h.ctx); err != nil {
			t.Fatal(err)
		}
		admittedTurn(t, h, c, conversation, fmt.Sprintf("probe-%d", i), "Check the computer.")
		if i == 0 {
			breached = agent.AgentID
		}
		c.Close()
	}
	h.stopCore(core)
	core = start(true)
	waitCoreReady(t, h)
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(h.ctx, `SELECT count(*) FROM naming_breaches WHERE person=? AND agent=?`, person, breached).Scan(&count); err != nil || count != 1 {
		t.Fatalf("breach did not persist exactly once: %d %v", count, err)
	}
	if err := db.QueryRowContext(h.ctx, `SELECT count(*) FROM naming_breaches WHERE agent<>?`, breached).Scan(&count); err != nil || count != 0 {
		t.Fatalf("breach charged another agent: %d %v", count, err)
	}
	if err := db.QueryRowContext(h.ctx, `SELECT count(*) FROM memory_item WHERE json_extract(record,'$.kind')='name' AND holder='all' AND json_extract(record,'$.revision')=2`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("correction did not retain common identity: %d %v", count, err)
	}
	h.stopCore(core)
	if calls.Load() != 12 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
}
