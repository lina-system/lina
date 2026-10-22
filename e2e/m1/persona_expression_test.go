// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"github.com/thisisjun786/lina/kit/protocol"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPersonaExpressionSamePromptSameFactsDifferentShape(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	var firstTools, firstInstructions string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input, Tools []json.RawMessage
			Instructions string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		tools := string(mustJSON(t, request.Tools))
		if n == 1 {
			firstTools = tools
			firstInstructions = request.Instructions
		} else if tools != firstTools || request.Instructions != firstInstructions {
			t.Error("emotion changed fixed instructions or offered authority")
		}
		if n == 2 {
			emitPersonaCall(w, "emotion", "record_emotion", map[string]any{"quote": "We finished the test run.", "valence": 1, "arousal": 1})
			return
		}
		if n == 3 {
			emitResponse(w, "Acknowledged.", true)
			return
		}
		var shape struct {
			Kind                               string
			Wording                            string
			TargetChars, TargetLines, MaxEmoji int
			Suppressed                         bool
		}
		found := false
		for _, raw := range request.Input {
			var item struct{ Role, Content string }
			if json.Unmarshal(raw, &item) != nil || item.Role != "developer" {
				continue
			}
			var candidate struct {
				Kind        string
				Wording     string
				TargetChars int `json:"target_chars"`
				TargetLines int `json:"target_lines"`
				MaxEmoji    int `json:"max_emoji"`
				Suppressed  bool
			}
			if json.NewDecoder(strings.NewReader(item.Content)).Decode(&candidate) == nil && candidate.Kind == "persona_expression" {
				found = true
				shape.Kind = candidate.Kind
				shape.Wording = candidate.Wording
				shape.TargetChars = candidate.TargetChars
				shape.TargetLines = candidate.TargetLines
				shape.MaxEmoji = candidate.MaxEmoji
				shape.Suppressed = candidate.Suppressed
			}
		}
		if !found || shape.TargetChars > 150 || shape.TargetLines > 3 {
			t.Errorf("missing/bad bounded expression: %+v", shape)
		}
		// The stub keeps its factual payload fixed and varies only its presentation
		// according to the actual prepared shape. It does not evaluate model quality.
		answer := "The result is 42."
		if n == 4 && (shape.Wording != "warm" || shape.MaxEmoji != 1) {
			t.Errorf("warm event not shaped: %+v", shape)
		}
		if shape.Wording == "warm" {
			answer = "Glad to help: the result is 42."
		}
		if shape.MaxEmoji > 0 {
			answer += " 🙂"
		}
		emitResponse(w, answer, true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	first := admittedTurn(t, h, c, conversation, "expression-first", "What is the result?")
	admittedTurn(t, h, c, conversation, "expression-event", "We finished the test run.")
	second := admittedTurn(t, h, c, conversation, "expression-second", "What is the result?")
	text := func(frames []protocol.Envelope) string {
		var out string
		for _, frame := range frames {
			if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerAgent {
				out += *item.Text
			}
		}
		return out
	}
	a, b := text(first), text(second)
	facts := regexp.MustCompile(`[0-9]+`)
	if a == b || len(a) == len(b) || strings.Contains(a, "🙂") || !strings.Contains(b, "🙂") || strings.Join(facts.FindAllString(a, -1), ",") != "42" || strings.Join(facts.FindAllString(b, -1), ",") != "42" {
		t.Fatalf("shape/facts mismatch: %q %q", a, b)
	}
	c.Close()
	h.stopCore(core)
	if calls.Load() != 4 {
		t.Fatalf("calls=%d", calls.Load())
	}
}
