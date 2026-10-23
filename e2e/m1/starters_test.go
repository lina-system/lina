// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/persona"
)

func TestM1EightStartersHeadless(t *testing.T) {
	t.Parallel()
	calls := map[string]int{}
	observations := make(chan string, 16)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Instructions string
			Input        []json.RawMessage
			Tools        []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		var identity struct {
			AgentID string `json:"agent_id"`
			Name    string
			Owner   string `json:"owner_person_id"`
			Layers  agents.Layers
		}
		if err := json.Unmarshal(instructionBlock(body.Instructions, "persona"), &identity); err != nil {
			t.Error(err)
			return
		}
		if identity.AgentID == "" || identity.Owner == "" || len(body.Tools) == 0 {
			t.Error("starter lost identity, owner or tools")
		}
		key := ""
		for _, s := range persona.Starters() {
			if s.Name == identity.Name {
				key = s.Key
			}
		}
		if key == "" {
			t.Errorf("unexpected starter name: %s", identity.Name)
			return
		}
		want := agents.Layers{Core: key + " fixture core-v1.md", DefaultVoice: key + " fixture default-voice-v1.md"}
		if key == "legal" {
			want = agents.Layers{}
		}
		if identity.Layers != want {
			t.Errorf("wrong working layers for %s: %+v", key, identity.Layers)
		}
		calls[key]++
		if calls[key] == 1 {
			observations <- string(mustJSON(t, body))
			emitPersonaCall(w, "starter-tool-"+key, "skill_index", map[string]any{})
		} else {
			var output struct{ Type, Output string }
			if len(body.Input) == 0 || json.Unmarshal(body.Input[len(body.Input)-1], &output) != nil || output.Type != "function_call_output" || !strings.Contains(output.Output, "persona-change") {
				t.Error("starter did not execute the real skill tool")
			}
			emitResponse(w, "Inventory checked for "+identity.Name+".", true)
		}
	}))
	defer provider.Close()
	binary := buildPersonaFixture(t)
	h := newHarness(t, binary)
	bundle := makeInstallBundle(t, h.root, binary, buildCommand(t, "lina-node"))
	_, start := installConversationBundle(t, h, bundle, provider.URL, "--persona-source", filepath.Join(h.root, "missing-private-world"))
	core := start(false)
	caps := []protocol.Capability{{Name: "agents", Version: "1.0"}, {Name: "memory", Version: "1.0"}}
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", caps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	receive := func(id string, err error) protocol.Envelope {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		for {
			e, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if e.ReplyTo != nil && *e.ReplyTo == id {
				if e.Error != nil {
					t.Fatalf("request refused: %s", e.Error.Code)
				}
				return e
			}
		}
	}
	agent := func(op string, id, name *string) protocol.AgentResult {
		t.Helper()
		return receive(c.Agent(h.ctx, protocol.AgentRequest{Operation: &op, AgentID: id, Name: name})).Payload.(protocol.AgentResult)
	}
	first := agent("list", nil, nil)
	if first.Agents == nil || len(*first.Agents) != 8 {
		t.Fatalf("fresh install has no eight starters: %+v", first)
	}
	ids := map[string]string{}
	for _, a := range *first.Agents {
		if a.Preset == nil || ids[*a.Preset] != "" {
			t.Fatalf("duplicate or absent preset: %+v", a)
		}
		ids[*a.Preset] = a.AgentID
	}
	for _, s := range persona.Starters() {
		found := false
		for _, a := range *first.Agents {
			if a.AgentID == ids[s.Key] && a.Name == s.Name {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing starter %s", s.Key)
		}
	}
	// Initialization needs no model account, selection step, or private checkout.
	if len(c.Welcome.Agents) != 1 || c.Welcome.Agents[0].AgentID != ids["lina"] || c.Welcome.Agents[0].Name != "Lina" {
		t.Fatalf("first surface does not open Lina: %+v", c.Welcome.Agents)
	}
	for _, language := range []design.Language{design.English, design.Korean} {
		terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=" + string(language)}, false, true, 140, 40)
		waitScreen(t, h, terminal, "/signin")
		h.keys(terminal, "/agents\r")
		waitScreen(t, h, terminal, "main:"+ids["system"])
		listed := screenText(terminal.screen.Snapshot())
		for _, s := range persona.Starters() {
			for _, want := range []string{s.Name, s.Key, design.Text(language, "agents.role."+s.Key), "main:" + ids[s.Key]} {
				if !strings.Contains(listed, want) {
					t.Fatalf("starter list omitted %s: %s", want, listed)
				}
			}
		}
		h.keys(terminal, "\x04")
		terminal.process.wait(t, 0)
		h.expectTerminalRestored(terminal)
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	var conversation string
	c, conversation = conversationClient(t, h, caps...)
	if conversation != "main:"+ids["lina"] {
		t.Fatal("first conversation is not Lina")
	}
	// Give every starter distinct personal knowledge through the real client API.
	for _, s := range persona.Starters() {
		id, op, holder, scope, content := ids[s.Key], "remember", "agent", "private", "starter-private-"+s.Key
		result := receive(c.Memory(h.ctx, protocol.MemoryRequest{Operation: &op, AgentID: &id, Holder: &holder, Scope: &scope, Text: &content})).Payload.(protocol.MemoryResult)
		if result.ItemID == nil {
			t.Fatal("personal knowledge was not saved")
		}
	}
	for _, s := range persona.Starters() {
		id := ids[s.Key]
		agent("choose", &id, nil)
		history := receive(c.History(h.ctx, protocol.HistoryGet{})).Payload.(protocol.History)
		if history.AgentID == nil || *history.AgentID != id || history.ConversationID == nil || *history.ConversationID != "main:"+id {
			t.Fatalf("wrong main conversation for %s: %+v", s.Key, history)
		}
		op := "list"
		memories := receive(c.Memory(h.ctx, protocol.MemoryRequest{Operation: &op, AgentID: &id})).Payload.(protocol.MemoryResult)
		if memories.Items == nil || len(*memories.Items) != 1 || (*memories.Items)[0].Content != "starter-private-"+s.Key {
			t.Fatalf("personal knowledge crossed identities for %s", s.Key)
		}
		conversation = *history.ConversationID
		topic := "conversation:" + conversation
		receive(c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}))
		frames := admittedTurn(t, h, c, conversation, "starter-turn-"+s.Key, "Check the skill inventory.")
		returned := false
		for _, f := range frames {
			if item, ok := f.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text == "Inventory checked for "+s.Name+"." {
				returned = true
			}
		}
		if !returned {
			t.Fatalf("tool-backed result missing for %s", s.Key)
		}
		select {
		case instructions := <-observations:
			if !strings.Contains(instructions, "starter-private-"+s.Key) {
				t.Fatalf("personal knowledge absent for %s", s.Key)
			}
			for _, other := range persona.Starters() {
				if other.Key != s.Key && strings.Contains(instructions, "starter-private-"+other.Key) {
					t.Fatalf("cross-agent recall: %s -> %s", other.Key, s.Key)
				}
			}
		case <-h.ctx.Done():
			t.Fatal("missing provider observation")
		}
		off := false
		receive(c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic, Active: &off}))
	}
	// Keep a user-created identity, a renamed starter and a deletion through restart.
	renamed, removed, chosen, name, custom := ids["knowledge"], ids["system"], ids["dev"], "Fixture renamed", "Fixture custom"
	agent("rename", &renamed, &name)
	agent("delete", &removed, nil)
	created := agent("create", nil, &custom)
	if created.AgentID == nil {
		t.Fatal("missing custom agent identity")
	}
	agent("choose", &chosen, nil)
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h, caps...)
	if conversation != "main:"+chosen || c.Welcome.Agents[0].AgentID != chosen || c.Welcome.Agents[0].Name != "Sion" {
		t.Fatal("startup did not resume the last chosen conversation")
	}
	for _, key := range []string{"lina", "dev"} {
		id := ids[key]
		agent("choose", &id, nil)
		history := receive(c.History(h.ctx, protocol.HistoryGet{})).Payload.(protocol.History)
		if history.ConversationID == nil || *history.ConversationID != "main:"+id {
			t.Fatalf("restart changed %s conversation", key)
		}
		topic := "conversation:" + *history.ConversationID
		snapshot := receive(c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})).Payload.(protocol.Snapshot)
		contents := string(mustJSON(t, snapshot.Items))
		for _, s := range persona.Starters() {
			if got := strings.Contains(contents, "Inventory checked for "+s.Name+"."); got != (s.Key == key) {
				t.Fatalf("conversation history crossed identities or was lost: %s, %s", key, s.Key)
			}
		}
	}
	after := agent("list", nil, nil)
	if after.Agents == nil || len(*after.Agents) != 8 {
		t.Fatal("restart duplicated or replaced starters")
	}
	foundCustom, foundRenamed := false, false
	for _, a := range *after.Agents {
		if a.AgentID == removed {
			t.Fatal("deleted starter recreated")
		}
		if a.AgentID == *created.AgentID {
			foundCustom = a.Name == custom
			continue
		}
		if a.Preset == nil || ids[*a.Preset] != a.AgentID {
			t.Fatalf("starter identity replaced: %+v", a)
		}
		if a.AgentID == renamed {
			foundRenamed = a.Name == name
		}
	}
	if !foundCustom || !foundRenamed {
		t.Fatal("existing custom or renamed agent lost")
	}
	for _, s := range persona.Starters() {
		if calls[s.Key] != 2 {
			t.Errorf("%s executed %d model calls; want one tool and its result", s.Key, calls[s.Key])
		}
	}
	op := "list"
	memories := receive(c.Memory(h.ctx, protocol.MemoryRequest{Operation: &op, AgentID: &chosen})).Payload.(protocol.MemoryResult)
	if memories.Items == nil || len(*memories.Items) != 1 || (*memories.Items)[0].Content != "starter-private-dev" {
		t.Fatal("restart lost personal knowledge")
	}
	c.Close()
	h.stopCore(core)
}
