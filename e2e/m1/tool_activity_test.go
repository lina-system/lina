// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1InstalledToolActivity(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		for _, raw := range request.Input {
			if strings.Contains(string(raw), `"tool_activity"`) || strings.Contains(string(raw), `"turn_state"`) || strings.Contains(string(raw), `"waiting_model"`) || strings.Contains(string(raw), "Listing skills") {
				t.Errorf("display activity entered model history: %s", raw)
			}
		}
		switch requests.Add(1) {
		case 1:
			emitPersonaCall(w, "fixture-tool", "skill_index", struct{}{})
		case 2:
			var item struct{ Type, Output string }
			var result struct {
				Code   string
				Output json.RawMessage
			}
			if len(request.Input) == 0 {
				t.Error("missing tool result")
				return
			}
			if err := json.Unmarshal(request.Input[len(request.Input)-1], &item); err != nil {
				t.Error(err)
			}
			if err := json.Unmarshal([]byte(item.Output), &result); err != nil {
				t.Error(err)
			}
			if item.Type != "function_call_output" || result.Code != "ok" || len(result.Output) == 0 {
				t.Errorf("tool did not actually run: %+v %+v", item, result)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Skills checked.\"}\n\n")
			w.(http.Flusher).Flush()
			emitResponse(w, "Skills checked.", true)
		default:
			emitResponse(w, "History checked.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "turn_activity", Version: "1.0"})
	legacy, _ := conversationClient(t, h)
	events := admittedTurn(t, h, c, conversation, "fixture-activity", "List available skills.")
	for {
		e, err := legacy.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(e.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"phase"`) || strings.Contains(string(raw), `"tool_name"`) || string(e.Kind) == "tool_activity" {
			t.Fatalf("unnegotiated activity reached legacy client: %s %s", e.Kind, raw)
		}
		if p, ok := e.Payload.(protocol.TurnState); ok && p.Active != nil && !*p.Active {
			break
		}
	}
	var steps []string
	var sawWaiting bool
	for _, event := range events {
		raw, err := json.Marshal(event.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var p struct {
			Phase, ToolName, CallID, Status, Outcome string
			Duration                                 *int64 `json:"duration_ms"`
		}
		// Wire names are deliberately decoded without the new Go types so this
		// acceptance test fails on the baseline for behavior, not compilation.
		var wire map[string]json.RawMessage
		if err = json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(wire["phase"], &p.Phase)
		json.Unmarshal(wire["tool_name"], &p.ToolName)
		json.Unmarshal(wire["call_id"], &p.CallID)
		json.Unmarshal(wire["status"], &p.Status)
		json.Unmarshal(wire["outcome"], &p.Outcome)
		json.Unmarshal(wire["duration_ms"], &p.Duration)
		if event.Kind == protocol.KindTurnState {
			if p.Phase == "waiting_model" {
				sawWaiting = true
			}
			if p.Phase == "tool" {
				if p.ToolName != "Listing skills" {
					t.Errorf("untrusted/non-display name %q", p.ToolName)
				}
				steps = append(steps, "tool")
			}
			if p.Phase == "streaming" {
				steps = append(steps, "streaming")
			}
		}
		if string(event.Kind) == "tool_activity" {
			if p.ToolName != "Listing skills" || p.CallID != "fixture-tool" {
				t.Errorf("tool identity: %s", raw)
			}
			if _, ok := wire["arguments"]; ok {
				t.Error("arguments leaked")
			}
			if _, ok := wire["output"]; ok {
				t.Error("output leaked")
			}
			if p.Status == "finished" {
				if p.Duration == nil || *p.Duration < 0 {
					t.Errorf("invalid duration: %s", raw)
				}
				steps = append(steps, "finished "+p.Outcome)
			} else {
				steps = append(steps, p.Status)
			}
		}
	}
	if want := []string{"tool", "started", "finished ok", "streaming"}; !reflect.DeepEqual(steps, want) || !sawWaiting {
		t.Fatalf("tool lifecycle=%v waiting_model=%v; want %v", steps, sawWaiting, want)
	}
	t.Logf("received lifecycle: %v; waiting_model=%v", steps, sawWaiting)
	// A later independent turn rebuilds history from durable storage.
	admittedTurn(t, h, c, conversation, "fixture-history", "Check history.")
	if requests.Load() != 3 {
		t.Fatalf("requests=%d", requests.Load())
	}
	topic := "conversation:" + conversation
	if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	for {
		e, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot, ok := e.Payload.(protocol.Snapshot); ok {
			for _, item := range snapshot.Items {
				if string(item.Kind) == "tool_activity" {
					t.Error("tool lifecycle retained as conversation snapshot item")
				}
			}
			break
		}
	}
	h.stopCore(core)
	reader, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var count int
	if err := reader.QueryRowContext(h.ctx, `SELECT count(*) FROM conv_event WHERE scaffold_kind IN ('turn_state','tool_activity') OR body LIKE '%"tool_activity"%' OR body LIKE '%Listing skills%'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("activity stored in conversation: count=%d err=%v", count, err)
	}
	if err := reader.QueryRowContext(h.ctx, `SELECT count(*) FROM turn_calls WHERE call_id='fixture-tool' AND json_extract(result,'$.code')='ok'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable actual tool result: count=%d err=%v", count, err)
	}
	t.Log("verified actual durable tool result; no activity in conversation storage or either model request")
}
