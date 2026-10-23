// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

// installedMessages harness state shared by the wire checks below.
type installedMessages struct {
	mu       sync.Mutex
	requests []map[string]any
	phase    int
}

func (f *installedMessages) capture(r *http.Request) (int, map[string]any) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, body)
	f.phase++
	return f.phase, body
}
func (f *installedMessages) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.requests...)
}

func messagesEvent(w http.ResponseWriter, typ, fields string) {
	fmt.Fprintf(w, "event: %s\ndata: {\"type\":%q%s}\n\n", typ, typ, fields)
}

// A real installed Core runs a Messages turn: the thinking block and its
// signature return unchanged inside the tool loop, the marks sit on Core's
// boundaries with the 1-hour lifetime, and the usage keeps cache reads and
// writes by lifetime.
func TestM2ClaudeAPIKeyTurnThroughRunningCore(t *testing.T) {
	fixture := &installedMessages{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "synthetic-key" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("credential or version missing: %v", r.Header)
		}
		n, body := fixture.capture(r)
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			markBoundaries(t, body)
			messagesEvent(w, "message_start", `,"message":{"id":"msg_1","role":"assistant","content":[],"usage":{"input_tokens":120,"cache_read_input_tokens":0,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":40},"output_tokens":4}}`)
			messagesEvent(w, "content_block_start", `,"index":0,"content_block":{"type":"thinking","thinking":"","signature":""}`)
			messagesEvent(w, "content_block_delta", `,"index":0,"delta":{"type":"thinking_delta","thinking":"Load the skill index."}`)
			messagesEvent(w, "content_block_delta", `,"index":0,"delta":{"type":"signature_delta","signature":"synthetic-signature-1"}`)
			messagesEvent(w, "content_block_stop", `,"index":0`)
			messagesEvent(w, "content_block_start", `,"index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"skill_index","input":{}}`)
			messagesEvent(w, "content_block_stop", `,"index":1`)
			messagesEvent(w, "message_delta", `,"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}`)
			messagesEvent(w, "message_stop", ``)
		default:
			// The follow-up request must carry the thinking block and its
			// signature exactly, and the tool result for the call above.
			messages := decodeWireMessages(t, body)
			if len(messages) < 3 {
				t.Fatalf("follow-up messages %d", len(messages))
			}
			assistant, last := messages[len(messages)-2], messages[len(messages)-1]
			if assistant.Role != "assistant" || len(assistant.Content) != 2 {
				t.Fatalf("assistant turn %+v", assistant)
			}
			thinking := assistant.Content[0]
			if thinking.Type != "thinking" || thinking.Thinking != "Load the skill index." || thinking.Signature != "synthetic-signature-1" {
				t.Fatalf("thinking not returned unchanged: %+v", thinking)
			}
			tool := assistant.Content[1]
			if tool.Type != "tool_use" || tool.ID != "toolu_1" || tool.Name != "skill_index" {
				t.Fatalf("tool use lost: %+v", tool)
			}
			if last.Role != "user" || len(last.Content) != 1 || last.Content[0].Type != "tool_result" || last.Content[0].ToolUseID != "toolu_1" {
				t.Fatalf("tool result lost: %+v", last)
			}
			messagesEvent(w, "message_start", `,"message":{"id":"msg_2","role":"assistant","content":[],"usage":{"input_tokens":90,"cache_read_input_tokens":80,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":2}}`)
			messagesEvent(w, "content_block_start", `,"index":0,"content_block":{"type":"text","text":""}`)
			messagesEvent(w, "content_block_delta", `,"index":0,"delta":{"type":"text_delta","text":"Skills are indexed."}`)
			messagesEvent(w, "content_block_stop", `,"index":0`)
			messagesEvent(w, "message_delta", `,"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}`)
			messagesEvent(w, "message_stop", ``)
		}
	}))
	t.Cleanup(server.Close)

	h, start := installedCore(t,
		"LINA_CLAUDE_API_MODEL=fixture-model",
		"LINA_CLAUDE_API_KEY=synthetic-key",
		"LINA_CLAUDE_API_BASE_URL="+server.URL+"/v1",
	)
	stop := start()
	defer stop()
	c, conversation := conversationClient(t, h)
	delivered := composeTurn(t, h, c, conversation, "messages-turn", "Check the available skills.")
	if !delivered["Skills are indexed."] {
		t.Fatalf("reply not delivered: %v", delivered)
	}
	c.Close()

	requests := fixture.sent()
	if len(requests) != 2 {
		t.Fatalf("requests %d", len(requests))
	}
	if model, _ := requests[0]["model"].(string); model != "fixture-model" {
		t.Fatalf("model %q", model)
	}
	if _, ok := requests[0]["stream"].(bool); !ok {
		t.Fatal("stream flag missing")
	}

	// The durable pre-send record names the capability and its lifetime.
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(h.ctx)
	var evidence ledger.RequestEvidence
	var raw, material string
	var revision int64
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT coalesce(body,''),coalesce(material_id,''),coalesce(material_revision,0) FROM conv_event WHERE scaffold_kind='request_assembly' ORDER BY rowid ASC LIMIT 1").Scan(&raw, &material, &revision)
	}); err != nil {
		t.Fatal(err)
	}
	if material != "" {
		// A large recorded request spills to the asset store.
		store, err := assets.Open(h.ctx, h.root, db)
		if err != nil {
			t.Fatal(err)
		}
		body, err := store.Read(h.ctx, material, revision)
		_ = store.Close()
		if err != nil {
			t.Fatal(err)
		}
		raw = string(body)
	}
	if err = json.Unmarshal([]byte(raw), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Class != provider.ModelAPI {
		t.Fatalf("ledger class %v", evidence.Class)
	}
	if evidence.Capabilities == nil || len(evidence.Capabilities.CacheMarks) == 0 {
		t.Fatalf("cache marks not recorded: %+v", evidence.Capabilities)
	}
	for _, mark := range evidence.Capabilities.CacheMarks {
		if mark.Lifetime != provider.Cache1Hour {
			t.Fatalf("lifetime %q", mark.Lifetime)
		}
	}
	if len(evidence.Capabilities.CacheMarks) > 4 {
		t.Fatal("more than four marks")
	}

	// The follow-up round keeps the same marks and the same 1-hour lifetime.
	markBoundaries(t, requests[1])
}

// markBoundaries checks the first request's cache marks against Core's layout
// evidence in the same wire: at most four, each 1 hour, never on the packet.
func markBoundaries(t *testing.T, body map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body["messages"])
	if n := strings.Count(string(raw), `"cache_control"`); n == 0 || n > 4 {
		t.Fatalf("marks %d: %s", n, raw)
	}
	if !strings.Contains(string(raw), `"ttl":"1h"`) {
		t.Fatalf("no 1-hour lifetime: %s", raw)
	}
	if strings.Contains(string(raw), `"ttl":"5m"`) {
		t.Fatalf("foreign lifetime: %s", raw)
	}
}

// A Core configured with a model but no key shows the sign-in line, sends no
// provider request, and does not move to another account.
func TestM2ClaudeAPIKeyMissingShowsSignIn(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	h, start := installedCore(t,
		"LINA_CLAUDE_API_MODEL=fixture-model",
		"LINA_CLAUDE_API_BASE_URL="+server.URL+"/v1",
	)
	stop := start()
	defer stop()
	c, conversation := conversationClient(t, h)
	defer c.Close()
	delivered := composeTurn(t, h, c, conversation, "messages-signed-out", "Try a turn without a key.")
	signIn := false
	for text := range delivered {
		signIn = signIn || strings.Contains(text, "Sign in")
	}
	if !signIn {
		t.Fatalf("refusal does not ask for sign-in: %v", delivered)
	}
	if requests != 0 {
		t.Fatalf("signed-out route sent %d requests", requests)
	}
}

// composeTurn submits one input and returns every text the turn delivered.
func composeTurn(t *testing.T, h *harness, c *clientapi.Client, conversation, key, text string) map[string]bool {
	t.Helper()
	id, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text})
	if err != nil {
		t.Fatal(err)
	}
	delivered := map[string]bool{}
	for {
		e, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if done, ok := e.Payload.(protocol.ItemDone); ok && done.Text != nil {
			delivered[*done.Text] = true
		}
		if e.ReplyTo != nil && *e.ReplyTo == id && e.Error != nil {
			t.Fatalf("input refused: %s", e.Error.Code)
		}
		if state, ok := e.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			return delivered
		}
	}
}

type wireBlock struct {
	CacheControl *struct {
		Type string `json:"type"`
		TTL  string `json:"ttl"`
	} `json:"cache_control"`
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

func decodeWireMessages(t *testing.T, body map[string]any) []wireMessage {
	t.Helper()
	raw, err := json.Marshal(body["messages"])
	if err != nil {
		t.Fatal(err)
	}
	var messages []wireMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	return messages
}

// The provider replay on this route records cache share and first-token time
// from the Messages usage and stream, through the installed command and Core.
func TestM2ClaudeAPIKeyProviderReplay(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []wireMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		round := (int(calls.Add(1))-1)%6 + 1
		w.Header().Set("Content-Type", "text/event-stream")
		// Turns two and four carry one recorded tool call, as the fixture says.
		if round == 2 || round == 5 {
			// The same arguments the fixture records.
			name, input := "skill_index", `{"methodology":"dev"}`
			if round == 5 {
				name, input = "skill_notices", `{}`
			}
			messagesEvent(w, "message_start", `,"message":{"id":"msg_r","role":"assistant","content":[],"usage":{"input_tokens":100,"cache_read_input_tokens":70,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":10},"output_tokens":1}}`)
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"recorded\",\"name\":%q,\"input\":%s}}\n\n", name, input)
			messagesEvent(w, "content_block_stop", `,"index":0`)
			messagesEvent(w, "message_stop", ``)
			return
		}
		if round == 3 || round == 6 {
			last := body.Messages[len(body.Messages)-1]
			if last.Role != "user" || len(last.Content) != 1 || last.Content[0].Type != "tool_result" || !strings.Contains(last.Content[0].Content, `"code":"ok"`) {
				t.Errorf("recorded tool result did not reach the provider: %+v", last)
			}
		}
		messagesEvent(w, "message_start", `,"message":{"id":"msg_r","role":"assistant","content":[],"usage":{"input_tokens":100,"cache_read_input_tokens":70,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":10},"output_tokens":1}}`)
		messagesEvent(w, "content_block_start", `,"index":0,"content_block":{"type":"text","text":""}`)
		messagesEvent(w, "content_block_delta", `,"index":0,"delta":{"type":"text_delta","text":"Public reply."}`)
		messagesEvent(w, "content_block_stop", `,"index":0`)
		messagesEvent(w, "message_stop", ``)
	}))
	t.Cleanup(server.Close)
	h, start := installedCore(t,
		"LINA_QA_REAL_MODEL=1",
		"LINA_CLAUDE_API_MODEL=fixture-model",
		"LINA_CLAUDE_API_KEY=synthetic-key",
		"LINA_CLAUDE_API_BASE_URL="+server.URL+"/v1",
	)
	stop := start()
	defer stop()
	// The replay command needs a ready Core, not only a listening socket.
	c, _ := conversationClient(t, h)
	c.Close()
	fixture, err := filepath.Abs("../../testdata/provider-replay/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(h.root, "provider-report")
	cmd := exec.CommandContext(h.ctx, filepath.Join(h.root, "system/bin/lina"), "provider-replay", "--provider", "claude", "--socket", h.socket, "--fixture", fixture, "--out", out)
	cmd.Env = append(os.Environ(), "LINA_QA_REAL_MODEL=1")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("provider-replay: %v\n%s\ncore: %s", err, raw, h.logs)
	}
	data, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report replay.ProviderReport
	if err = json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	// Six turn requests and the trailing compaction summary.
	if report.Failure != "" || report.Provider != "claude" || report.Route != "claude_api_key" || len(report.Requests) != 7 || report.Requests[6].Turn != "summary" {
		t.Fatalf("report: %+v", report)
	}
	for _, row := range report.Requests {
		if row.FirstOutputMS == nil {
			t.Fatalf("first output not measured: %+v", row)
		}
		if row.Input == nil || *row.Input != 180 || row.Read == nil || *row.Read != 70 || row.Plain == nil || *row.Plain != 100 {
			t.Fatalf("usage: %+v", row)
		}
		if row.CacheShare == nil || *row.CacheShare < 0.38 || *row.CacheShare > 0.39 {
			t.Fatalf("cache share: %+v", row)
		}
		if write := row.WrittenByLifetime[provider.Cache1Hour]; write == nil || *write != 10 {
			t.Fatalf("lifetime writes: %+v", row.WrittenByLifetime)
		}
	}
}
