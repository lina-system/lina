// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/memory"
)

func TestPersonaSixTurnConversation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input        []json.RawMessage
			Instructions string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		var persona struct {
			Layers agents.Layers `json:"layers"`
		}
		if err := json.Unmarshal(instructionBlock(request.Instructions, "persona"), &persona); err != nil {
			t.Error(err)
		}
		wantVoice := ""
		if n >= 5 && n <= 10 {
			wantVoice = "fixture casual voice"
		}
		if persona.Layers.UserVoice != wantVoice {
			t.Errorf("request %d voice %q want %q", n, persona.Layers.UserVoice, wantVoice)
		}
		if n >= 3 && emotionPacket(t, request.Input).Short.Valence != 0.8 {
			t.Errorf("request %d lost confirmed emotion", n)
		}
		relationship := false
		filled := false
		for _, raw := range request.Input {
			var item struct{ Role, Content string }
			if json.Unmarshal(raw, &item) != nil {
				continue
			}
			if strings.Contains(item.Content, "persona_settings") && strings.Contains(item.Content, "violet") {
				filled = true
			}
			if strings.Contains(item.Content, "relationship_view") && strings.Contains(item.Content, "We are study partners.") {
				relationship = true
				if item.Role != "user" {
					t.Error("relationship acquired instruction authority")
				}
			}
		}
		if n >= 7 && !filled {
			t.Error("filled blank absent from next turn")
		}
		if n == 11 && !relationship {
			t.Error("relationship absent from Atropos packet")
		}
		if n <= 8 && relationship {
			t.Error("relationship changed inside prepared turn")
		}
		switch n {
		case 1:
			emitPersonaCall(w, "emotion", "record_emotion", map[string]any{"quote": "We finished the test run.", "valence": 0.8, "arousal": 0.4})
		case 3:
			emitPersonaCall(w, "voice", "change_persona", map[string]any{"quote": "Talk more casually.", "user_voice": "fixture casual voice"})
		case 5:
			emitPersonaCall(w, "blank", "fill_persona_setting", map[string]any{"quote": "Your favorite color is violet.", "name": "likes", "value": "violet"})
		case 7:
			emitPersonaCall(w, "relationship", "record_relationship", map[string]any{"quote": "We are study partners."})
		case 9:
			emitPersonaCall(w, "undo", "undo_persona", map[string]any{"quote": "Undo that voice change.", "revision": 2})
		default:
			if n < 11 {
				var result struct{ Output string }
				json.Unmarshal(request.Input[len(request.Input)-1], &result)
				if !strings.Contains(result.Output, `"code":"ok"`) {
					t.Errorf("request %d tool failed: %s", n, result.Output)
				}
			}
			emitResponse(w, "Acknowledged.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, _ := conversationClient(t, h)
	person := c.Welcome.PersonID
	c.Close()
	h.stopCore(core)
	agent := seedPersonaAgent(t, h, person)
	core = start(true)
	c, conversation := conversationClient(t, h)
	turns := []string{"We finished the test run.", "Talk more casually.", "Your favorite color is violet.", "We are study partners.", "Undo that voice change.", "What do you remember?"}
	for i, text := range turns {
		admittedTurn(t, h, c, conversation, []string{"six-emotion", "six-voice", "six-blank", "six-relationship", "six-undo", "six-read"}[i], text)
		if i == 4 {
			c.Close()
			h.stopCore(core)
			core = start(true)
			c, conversation = conversationClient(t, h)
		}
	}
	reader, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var raw []byte
	var revision int64
	if err := reader.QueryRowContext(h.ctx, "SELECT state,revision FROM agents WHERE id=?", agent).Scan(&raw, &revision); err != nil {
		t.Fatal(err)
	}
	var state agents.State
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if revision != 3 || state.Settings["likes"] != "violet" || state.Affect == nil || state.Affect.Short[conversation].EventID == "" {
		t.Fatalf("six-turn persona: revision=%d state=%+v", revision, state)
	}
	if err := reader.QueryRowContext(h.ctx, "SELECT record FROM memory_item WHERE holder=? AND json_extract(record,'$.kind')='relationship'", agent).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var item memory.Item
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	if item.Person != person || item.Subject.ID != person || item.Content != turns[3] || len(item.Evidence) != 1 {
		t.Fatalf("relationship memory: %+v", item)
	}
	for _, source := range []string{state.Affect.Short[conversation].EventID, state.SettingSources["likes"], item.Evidence[0].ID} {
		var speaker, owner, sourceAgent string
		if err := reader.QueryRowContext(h.ctx, "SELECT speaker_kind,person_id,agent_id FROM conv_event WHERE id=?", source).Scan(&speaker, &owner, &sourceAgent); err != nil {
			t.Fatal(err)
		}
		if speaker != "person" || owner != person || sourceAgent != agent {
			t.Fatal("persona/memory evidence is not native owner input")
		}
	}
	c.Close()
	h.stopCore(core)
	if calls.Load() != 11 {
		t.Fatalf("six person turns should have eleven provider requests, got %d", calls.Load())
	}
}
