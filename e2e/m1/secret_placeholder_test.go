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

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1SecretPlaceholderInstalledCore(t *testing.T) {
	t.Parallel()
	var scenario struct {
		Input, Reply string
		MaskedInput  string `json:"masked_input"`
		MaskedReply  string `json:"masked_reply"`
		Forbidden    []string
	}
	data, err := os.ReadFile("secret-placeholder.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	assertSafe := func(value any) {
		t.Helper()
		encoded := string(mustJSON(t, value))
		for _, forbidden := range scenario.Forbidden {
			if strings.Contains(encoded, forbidden) {
				t.Error("credential suffix reached a downstream consumer")
			}
		}
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		assertSafe(request.Input)
		if !strings.Contains(string(mustJSON(t, request.Input)), "INPUT-269") {
			t.Error("provider request lost public input")
		}
		calls.Add(1)
		emitResponse(w, scenario.Reply, true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	frames := admittedTurn(t, h, c, conversation, "placeholder", scenario.Input)
	inputSeen, replySeen := false, false
	for _, frame := range frames {
		assertSafe(frame)
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil {
			inputSeen = inputSeen || *item.Text == scenario.MaskedInput
			replySeen = replySeen || *item.Text == scenario.MaskedReply
		}
	}
	if !inputSeen || !replySeen {
		t.Error("live conversation did not retain masked input and reply punctuation")
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	defer h.stopCore(core)
	c, conversation = conversationClient(t, h)
	defer c.Close()
	topic := "conversation:" + conversation
	if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if !ok {
		t.Fatal("no restored conversation snapshot")
	}
	assertSafe(snapshot)
	encoded := string(mustJSON(t, snapshot))
	for _, want := range []string{scenario.MaskedInput, scenario.MaskedReply} {
		if !strings.Contains(encoded, string(mustJSON(t, want))) {
			t.Error("restart lost masked history or punctuation")
		}
	}
	// Read the restored conversation into a new model request as well.
	for _, frame := range admittedTurn(t, h, c, conversation, "follow-up", "Continue.") {
		assertSafe(frame)
	}
	if calls.Load() != 2 {
		t.Fatal("expected one provider call per admitted turn, without restart replay")
	}
}
