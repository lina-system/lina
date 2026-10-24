// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// gatewayFixture is a Messages-compatible model gateway that answers only on
// its own API prefix.
type gatewayFixture struct {
	mu      sync.Mutex
	paths   []string
	keys    []string
	headers []http.Header
}

func (f *gatewayFixture) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.keys = append(f.keys, r.Header.Get("x-api-key"))
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		if r.URL.Path != "/gateway/anthropic/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, messagesReply("Gateway answered."))
	}))
	t.Cleanup(server.Close)
	return server
}

func (f *gatewayFixture) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *gatewayFixture) requestKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

func messagesReply(text string) string {
	return `event: message_start` + "\n" +
		`data: {"type":"message_start","message":{"id":"msg_gateway","role":"assistant","content":[],"usage":{"input_tokens":11,"output_tokens":1}}}` + "\n\n" +
		`event: content_block_start` + "\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`event: content_block_delta` + "\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + fmt.Sprintf("%q", text) + `}}` + "\n\n" +
		`event: content_block_stop` + "\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`event: message_delta` + "\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
		`event: message_stop` + "\n" +
		`data: {"type":"message_stop"}` + "\n\n"
}

// A confirmed gateway (base URL and key) serves turns through installed Core.
// The vendor command's own gateway setup in Core's environment is neither
// adopted nor used.
func TestM2ConfirmedClaudeGatewayAnswersThroughRunningCore(t *testing.T) {
	gateway := &gatewayFixture{}
	server := gateway.serve(t)
	var decoyCount int
	var decoyMu sync.Mutex
	decoy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decoyMu.Lock()
		decoyCount++
		decoyMu.Unlock()
		http.Error(w, "unconfirmed gateway reached", http.StatusBadRequest)
	}))
	t.Cleanup(decoy.Close)

	h, start := installedCore(t,
		"LINA_CLAUDE_API_MODEL=gateway-model",
		"LINA_CLAUDE_API_KEY=confirmed-gateway-key",
		"LINA_CLAUDE_API_BASE_URL="+server.URL+"/gateway/anthropic/v1",
		// The command's own gateway login: present, never confirmed.
		"ANTHROPIC_BASE_URL="+decoy.URL+"/v1",
		"ANTHROPIC_API_KEY=command-key",
	)
	stop := start()
	defer stop()
	c, conversation := conversationClient(t, h)
	defer c.Close()
	for _, turn := range []string{"gateway-turn-1", "gateway-turn-2"} {
		delivered := composeTurn(t, h, c, conversation, turn, "Answer through the gateway.")
		if !delivered["Gateway answered."] {
			t.Fatalf("turn %s not answered: %v", turn, delivered)
		}
	}

	paths, keys := gateway.requests(), gateway.requestKeys()
	if len(paths) != 2 || len(keys) != 2 {
		t.Fatalf("gateway requests %v", paths)
	}
	for i := range paths {
		if paths[i] != "/gateway/anthropic/v1/messages" {
			t.Fatalf("request %d reached %q", i, paths[i])
		}
		if keys[i] != "confirmed-gateway-key" {
			t.Fatalf("request %d carried key %q", i, keys[i])
		}
		if header := gateway.headers[i].Get("Authorization"); strings.Contains(header, "command") {
			t.Fatalf("request %d carried the command credential", i)
		}
	}
	decoyMu.Lock()
	defer decoyMu.Unlock()
	if decoyCount != 0 {
		t.Fatalf("unconfirmed gateway received %d requests", decoyCount)
	}
}
