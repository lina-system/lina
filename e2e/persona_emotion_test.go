// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPersonaEmotionIsPreparedOutsideCachedPrefix(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Instructions string
			Input        []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		calls.Add(1)
		if strings.Contains(request.Instructions, "persona_emotion") {
			t.Error("dynamic emotion entered cached instructions")
		}
		found := false
		for i, raw := range request.Input {
			var item struct{ Role, Content string }
			if json.Unmarshal(raw, &item) == nil && strings.Contains(item.Content, "persona_emotion") {
				found = true
				if i >= len(request.Input)-1 || item.Role != "developer" {
					t.Error("emotion must precede current input in the ContextPacket")
				}
			}
		}
		if !found {
			t.Error("turn preparation omitted emotion from ContextPacket")
		}
		emitResponse(w, "The result is 42.", true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "emotion-packet", "What is the result?")
	c.Close()
	h.stopCore(core)
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d", calls.Load())
	}
}

// Context records precede the reference footer; decode only the first JSON value.
func emotionPacket(t *testing.T, items []json.RawMessage) agents.EmotionSnapshot {
	t.Helper()
	for _, raw := range items {
		var item struct{ Role, Content string }
		if json.Unmarshal(raw, &item) != nil || item.Role != "developer" {
			continue
		}
		var data struct {
			Kind  string
			State agents.EmotionSnapshot
		}
		if json.NewDecoder(strings.NewReader(item.Content)).Decode(&data) == nil && data.Kind == "persona_emotion" {
			return data.State
		}
	}
	t.Error("missing emotion record")
	return agents.EmotionSnapshot{}
}

func TestPersonaEmotionConfirmedWriteSuppressionAndRestart(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		state := emotionPacket(t, request.Input)
		switch n {
		case 1, 2:
			if state.Short != (agents.Affect{}) {
				t.Error("current turn changed emotion packet")
			}
		case 3:
			if state.Suppressed || state.Short.Valence != 0.8 || state.Mood.Valence <= 0 {
				t.Errorf("confirmed emotion missing: %+v", state)
			}
		case 4, 7:
			if !state.Suppressed || state.Short != (agents.Affect{}) || state.Mood != (agents.Affect{}) {
				t.Errorf("suppression failed: %+v", state)
			}
		}
		switch n {
		case 1:
			emitPersonaCall(w, "emotion", "record_emotion", map[string]any{"quote": "We finished the test run.", "valence": 0.8, "arousal": 0.4})
		case 5:
			emitPersonaCall(w, "off", "set_emotion_expression", map[string]any{"quote": "Turn emotional expression off.", "enabled": false})
		default:
			if n == 2 || n == 6 {
				var result struct{ Output string }
				json.Unmarshal(request.Input[len(request.Input)-1], &result)
				if !strings.Contains(result.Output, `"code":"ok"`) {
					t.Errorf("emotion tool failed: %s", result.Output)
				}
			}
			emitResponse(w, "Acknowledged.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	person := c.Welcome.PersonID
	admittedTurn(t, h, c, conversation, "emotion-event", "We finished the test run.")
	admittedTurn(t, h, c, conversation, "emotion-read", "What happened?")
	admittedTurn(t, h, c, conversation, "emotion-sensitive", "I need help with a medical diagnosis.")
	admittedTurn(t, h, c, conversation, "emotion-off", "Turn emotional expression off.")
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "emotion-restart", "What happened?")
	reader, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var raw []byte
	if err := reader.QueryRowContext(h.ctx, "SELECT state FROM agents WHERE id=(SELECT agent_id FROM agent_first_run WHERE singleton=1)").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state agents.State
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Affect == nil || !state.ExpressionOff || state.Affect.Short[conversation].Affect.Valence != 0.8 {
		t.Fatalf("durable emotion: %+v", state)
	}
	source := state.Affect.Short[conversation].EventID
	var speaker, owner string
	if err := reader.QueryRowContext(h.ctx, "SELECT speaker_kind,person_id FROM conv_event WHERE id=?", source).Scan(&speaker, &owner); err != nil {
		t.Fatal(err)
	}
	if speaker != "person" || owner != person {
		t.Fatal("emotion has no confirmed native owner event")
	}
	c.Close()
	h.stopCore(core)
	if calls.Load() != 7 {
		t.Fatalf("calls=%d", calls.Load())
	}
}
