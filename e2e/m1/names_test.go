// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestNamesReachEveryAgentAndCountRetiredAliases(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	type request struct {
		Input, Tools []json.RawMessage
		Instructions string
	}
	var mu sync.Mutex
	var originals []request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		mu.Lock()
		if n >= 5 && n <= 12 {
			originals = append(originals, req)
		} else if n >= 13 && n <= 20 && !reflect.DeepEqual(originals[n-13], req) {
			t.Error("shared-name replay changed the original request")
		}
		mu.Unlock()
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
	createdIn := conversation
	var correctedIn string
	admittedTurn(t, h, c, conversation, "name-create", "The desk computer is Cedar. Creation-only detail.")
	if calls.Load() != 2 {
		t.Fatal("name creation did not execute its tool")
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
	// Correct from a different agent so every replay must validate common
	// evidence from more than one conversation without exporting those turns.
	for _, agent := range *list.Agents {
		other := "main:" + agent.AgentID
		if other == conversation {
			continue
		}
		topic := "conversation:" + other
		if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Receive(h.ctx); err != nil {
			t.Fatal(err)
		}
		correctedIn = other
		admittedTurn(t, h, c, other, "name-correct", "The desk computer is Birch, not Cedar. Correction-only detail.")
		break
	}
	if calls.Load() != 4 {
		t.Fatal("name correction did not execute its tool")
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
	for i, agent := range *list.Agents {
		p := h.command("export", "--socket", h.socket, "--agent", agent.AgentID, "--conversation", "main:"+agent.AgentID, "--since", "2020-01-01T00:00:00Z", "--until", "2100-01-01T00:00:00Z")
		p.start(t)
		p.wait(t, 0)
		var out protocol.ExportResult
		if err := json.Unmarshal(p.stdout.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(out.Directory, "episodes.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		var probe replay.Episode
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var ep replay.Episode
			if json.Unmarshal([]byte(line), &ep) == nil && ep.Input.Text == "Check the computer." {
				probe = ep
			}
		}
		if probe.TurnID == "" {
			t.Fatalf("shared-name probe excluded for agent %s: %s", agent.AgentID, out.Manifest)
		}
		encoded := string(mustJSON(t, probe))
		if probe.Conversation != createdIn && strings.Contains(encoded, "Creation-only detail.") || probe.Conversation != correctedIn && strings.Contains(encoded, "Correction-only detail.") {
			t.Fatal("shared name disclosed another agent's source text")
		}
		p = h.command("replay", probe.TurnID, "--socket", h.socket, "--out", filepath.Join(h.root, fmt.Sprintf("name-replay-%d", i)))
		p.start(t)
		p.wait(t, 0)
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
	if calls.Load() != 20 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
}
