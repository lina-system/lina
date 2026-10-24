// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func textCallReply(w http.ResponseWriter, text string) { textCallReplyAt(w, text, 0) }
func textCallReplyAt(w http.ResponseWriter, text string, index int) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, chunk := range []string{text[:1], text[1:]} {
		raw, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":%d,\"delta\":%s}\n\n", index, raw)
	}
	item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": text}}})
	fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", index, item)
	fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
}

func TestTextCallsThroughRunningCore(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		attempts   int
		failed     bool
	}{
		{"complete", "[tool_use id=synthetic name=_load_tools]\n{\"tools\":[\"calculate\"]}", 3, false},
		{"complete_runtime", "[tool_use id=synthetic name=_load_tools]\n{\"tools\":[\"calculate\"]}", 3, false},
		{"prose", "The marker `[tool_use` denotes a tool call.", 1, false},
		{"real", "[tool_use name=not_declared]\n{}", 2, false},
		{"incomplete", "[tool_use id=synthetic name=_load_tools]\n{\"tools\":", 2, false},
		{"malformed", "[tool_use id=synthetic name=_load_tools]\n{broken}", 2, false},
		{"disallowed", "[tool_use id=synthetic name=not_declared]\n{}", 2, false},
		{"repeated", "[tool_use", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			requests := 0
			control := tc.name == "prose" || tc.name == "real"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := decodeRequest(t, r)
				mu.Lock()
				defer mu.Unlock()
				requests++
				for _, item := range q.Input {
					if !control && strings.Contains(string(item), "[tool_use") {
						t.Error("contaminated request history")
					}
				}
				switch {
				case requests == 1 || tc.failed:
					if tc.name == "real" {
						raw, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": tc.text}}})
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":%s}\n\n", raw)
						toolCall(w, "real-load", "load_tools", map[string]any{"tools": []string{"calculate"}})
					} else if tc.name == "incomplete" {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"old-reasoning\",\"summary\":[]}}\n\n")
						textCallReplyAt(w, tc.text, 1)
					} else {
						textCallReply(w, tc.text)
					}
				case strings.HasPrefix(tc.name, "complete") && requests == 2:
					if got := last(q); got.Type != "function_call_output" || !strings.Contains(got.Output, `"code":"ok"`) {
						t.Errorf("missing authorized loader result: %+v", got)
					}
					textCallReply(w, "[tool_use id=calculate name=_calculate]\n{\"expression\":\"6*7\"}")
				default:
					if strings.HasPrefix(tc.name, "complete") {
						if got := last(q); !strings.Contains(got.Output, `\"exact_value\":\"42\"`) && !strings.Contains(got.Output, `"exact_value":"42"`) {
							t.Errorf("missing calculation result: %+v", got)
						}
					}
					reply(w, "The result is 42.")
				}
			}))
			defer server.Close()
			h := newCore(t, server.URL, func(route *modelpath.Config) {
				if tc.name == "complete_runtime" {
					route.Adapter = runtimeFixtureAdapter{route.Adapter}
				}
			})
			agent := h.agent("lina")
			c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "text-call-test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			conversation := "main:" + agent
			topic := "conversation:" + conversation
			if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Receive(h.ctx); err != nil {
				t.Fatal(err)
			}
			key, input := tc.name, "Calculate six times seven."
			id, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &input})
			if err != nil {
				t.Fatal(err)
			}
			ack, done := false, false
			for !ack || !done {
				frame, err := c.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(frame.Payload)
				if !control && strings.Contains(string(raw), "[tool_use") {
					t.Errorf("visible text call: %s", raw)
				}
				if frame.ReplyTo != nil && *frame.ReplyTo == id {
					ack = true
				}
				if s, ok := frame.Payload.(protocol.TurnState); ok && s.Active != nil && !*s.Active {
					done = true
				}
			}
			mu.Lock()
			count := requests
			mu.Unlock()
			if count != tc.attempts {
				t.Fatalf("requests=%d want=%d", count, tc.attempts)
			}
			h.read(func(tx *canon.Tx) error {
				var state string
				if err := tx.QueryRowContext(h.ctx, "SELECT state FROM turn_runs WHERE conversation=?", conversation).Scan(&state); err != nil {
					return err
				}
				want := "completed"
				if tc.failed {
					want = "failed"
				}
				if state != want {
					t.Errorf("state=%s want=%s", state, want)
				}
				var contaminated int
				if err := tx.QueryRowContext(h.ctx, "SELECT count(*) FROM turn_items WHERE cast(item AS TEXT) LIKE '%[tool_use%' ").Scan(&contaminated); err != nil {
					return err
				}
				if !control && contaminated != 0 {
					t.Errorf("persisted text calls=%d", contaminated)
				}
				return nil
			})
		})
	}
}

func TestTextCallsBackgroundThroughRunningCore(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		calls      int
		failed     bool
	}{
		{"complete", "[tool_use name=_skill_index]\n{}", 1, false},
		{"incomplete", "[tool_use name=_skill_index]\n{", 0, false},
		{"disallowed", "[tool_use name=not_declared]\n{}", 0, false},
		{"repeated", "[tool_use", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			conversationRequests, runRequests := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := decodeRequest(t, r)
				mu.Lock()
				defer mu.Unlock()
				for _, item := range q.Input {
					if strings.Contains(string(item), "[tool_use") {
						t.Error("contaminated run or conversation history")
					}
				}
				if strings.HasPrefix(q.Instructions, "Execute only the self-contained background brief") {
					runRequests++
					if runRequests == 1 || tc.failed {
						textCallReply(w, tc.text)
						return
					}
					if tc.calls > 0 && !strings.Contains(last(q).Output, `"code":"ok"`) {
						t.Errorf("missing real read receipt: %+v", last(q))
					}
					reply(w, `{"body":"Fixture skills inspected.","done":true}`)
					return
				}
				conversationRequests++
				switch conversationRequests {
				case 1:
					toolCall(w, "load-run", "load_tools", map[string]any{"tools": []string{"start_run"}})
				case 2:
					toolCall(w, "start-run", "start_run", map[string]any{"Goal": "Inspect fixture skills", "Scope": "Skill index", "NotTouch": "Other data", "Done": "Report available skills", "Checks": []map[string]string{{"Name": "skill_index", "Kind": "tool_called"}}, "Result": "summary", "Allow": []string{"skill_index"}})
				default:
					reply(w, "Background work recorded.")
				}
			}))
			defer server.Close()
			h := newCore(t, server.URL)
			h.turn(h.agent("lina"), tc.name, "Inspect fixture skills in the background.")
			h.await(`SELECT count(*) FROM background_runs WHERE json_extract(record,'$.State')='landed'`, 1)
			mu.Lock()
			count := runRequests
			mu.Unlock()
			if count != 2 {
				t.Fatalf("run requests=%d want=2", count)
			}
			h.read(func(tx *canon.Tx) error {
				var raw []byte
				if err := tx.QueryRowContext(h.ctx, `SELECT record FROM background_runs`).Scan(&raw); err != nil {
					return err
				}
				var record struct {
					Outcome  string
					Calls    []string
					Reason   string
					Artifact struct{ Body string }
				}
				if err := json.Unmarshal(raw, &record); err != nil {
					return err
				}
				if strings.Contains(string(raw), "[tool_use") {
					t.Error("contaminated run record")
				}
				if len(record.Calls) != tc.calls {
					t.Errorf("calls=%v", record.Calls)
				}
				if tc.failed {
					if record.Outcome != "failed" || record.Reason != "text_tool_call_recovery_failed" {
						t.Errorf("failure=%s %s", record.Outcome, record.Reason)
					}
				} else if record.Artifact.Body != "Fixture skills inspected." {
					t.Errorf("artifact=%+v", record.Artifact)
				}
				return nil
			})
		})
	}
}

// The fixture uses the same scripted wire with AgentRuntime fidelity to exercise
// Core's bridge-enabled loop and recovered calls through its normal dispatcher.
type runtimeFixtureAdapter struct{ provider.Adapter }

func (runtimeFixtureAdapter) Class() provider.Class { return provider.AgentRuntime }

func (a runtimeFixtureAdapter) Stream(ctx context.Context, l lease.Lease, req provider.Request) (provider.Stream, error) {
	// The Responses fixture transports text only; Core still installs the runtime
	// bridge and dispatches recovered text calls through its ordinary tool path.
	req.Bridge = nil
	stream, err := a.Adapter.Stream(ctx, l, req)
	if err != nil {
		return nil, err
	}
	return runtimeFixtureStream{stream}, nil
}

type runtimeFixtureStream struct{ provider.Stream }

func (s runtimeFixtureStream) Meta() provider.Meta {
	m := s.Stream.Meta()
	m.Class = provider.AgentRuntime
	return m
}
