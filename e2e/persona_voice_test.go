// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func emitPersonaCall(w http.ResponseWriter, id, name string, args any) {
	raw, _ := json.Marshal(args)
	item, _ := json.Marshal(map[string]any{"type": "function_call", "id": "item-" + id, "call_id": id, "name": name, "arguments": string(raw)})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
	fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
}

func TestPersonaChangeByAskingAndUndoAcrossRestart(t *testing.T) {
	var calls atomic.Int32
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
		var persona struct {
			Layers struct {
				UserVoice string `json:"user_voice"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(instructionBlock(request.Instructions, "persona"), &persona); err != nil {
			t.Error(err)
		}
		want := ""
		if n >= 3 && n <= 5 {
			want = "fixture casual voice"
		}
		if persona.Layers.UserVoice != want {
			t.Errorf("request %d applied voice %q; want %q", n, persona.Layers.UserVoice, want)
		}
		toolNames := map[string]bool{}
		for _, raw := range request.Tools {
			var tool struct{ Name string }
			json.Unmarshal(raw, &tool)
			toolNames[tool.Name] = true
		}
		if !toolNames["change_persona"] || !toolNames["undo_persona"] {
			t.Error("persona tools not offered")
		}
		switch n {
		case 1:
			emitPersonaCall(w, "change", "change_persona", map[string]any{"quote": "talk more casually", "user_voice": "fixture casual voice"})
		case 4:
			emitPersonaCall(w, "undo", "undo_persona", map[string]any{"quote": "undo that change", "revision": 2})
		case 2, 5:
			var output struct{ Type, Output string }
			if err := json.Unmarshal(request.Input[len(request.Input)-1], &output); err != nil {
				t.Error(err)
			}
			if output.Type != "function_call_output" || !strings.Contains(output.Output, `"code":"ok"`) {
				t.Errorf("persona write failed: %s", output.Output)
			}
			emitResponse(w, "Acknowledged.", true)
		default:
			emitResponse(w, "The answer is 42.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	notices := func(frames []protocol.Envelope) int {
		count := 0
		for _, frame := range frames {
			if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerSystem && item.Text != nil {
				count++
				if strings.Contains(*item.Text, "\n") {
					t.Error("persona notice is not one line")
				}
			}
		}
		return count
	}
	if n := notices(admittedTurn(t, h, c, conversation, "voice-change", "Please talk more casually")); n != 1 {
		t.Errorf("change notices = %d", n)
	}
	admittedTurn(t, h, c, conversation, "voice-next", "What is the answer?")
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	if n := notices(admittedTurn(t, h, c, conversation, "voice-undo", "Please undo that change")); n != 1 {
		t.Errorf("undo notices = %d", n)
	}
	admittedTurn(t, h, c, conversation, "voice-restored", "What is the answer again?")
	c.Close()
	h.stopCore(core)
	if calls.Load() != 6 {
		t.Fatalf("provider calls = %d", calls.Load())
	}
}
