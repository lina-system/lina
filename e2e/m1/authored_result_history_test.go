// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestAuthoredResultContentAndHistoryInstalledCore(t *testing.T) {
	long := strings.Repeat("결과 content.\n", 6000) + "RESULT-END"
	for _, tc := range []struct {
		kind result.Type
		body any
	}{
		{result.DraftType, map[string]any{"blocks": []any{map[string]any{"kind": "paragraph", "text": long}}, "from_summary": nil}},
		{result.TableType, map[string]any{"columns": []any{map[string]any{"name": "Content", "type": "text", "unit": ""}}, "rows": [][]string{{long}}}},
		{result.PlanType, map[string]any{"entries": []any{map[string]any{"id": "step", "kind": "todo", "title": "Check the result", "state": "planned", "parent": nil}}, "confirm": nil}},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := result.Validate(tc.kind, body, result.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			const summary = "The result is ready."
			wantText := summary + "\n" + result.PlainText(decoded, nil)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch calls.Add(1) {
				case 1:
					emitPersonaCall(w, "authored-result", "present_result", result.PresentRequest{
						Type: tc.kind, Title: "Authored result", Summary: summary, Body: body,
						Completion: &result.CompletionRequest{WorkKind: "document"},
					})
				case 2:
					emitResponse(w, "Presented.", true)
				default:
					t.Error("history restoration called the provider")
					emitResponse(w, "Unexpected.", true)
				}
			}))
			defer server.Close()
			h, start := installedConversationHarness(t, server.URL)
			core := start(true)
			c, conversation := conversationClient(t, h)
			frames := admittedTurn(t, h, c, conversation, "authored-result", "Present the result.")
			var live protocol.Result
			for _, frame := range frames {
				if card, ok := frame.Payload.(protocol.Result); ok {
					live = card
				}
			}
			providerCalls := calls.Load()
			check := func(phase, key string, card protocol.Result) {
				t.Helper()
				if card.ResultID == "" || card.Type != protocol.ResultType(tc.kind) || key != "result-card:"+card.ResultID {
					t.Errorf("%s: result identity/type changed: key=%q id=%q type=%s", phase, key, card.ResultID, card.Type)
				}
				if card.Text == nil {
					t.Fatal("missing result text")
				}
				full := *card.Text
				for next := card.Next; next != nil; {
					request, err := c.Result(h.ctx, protocol.ResultGet{ResultID: card.ResultID, Revision: &card.Revision, ConversationID: &conversation, Offset: next})
					if err != nil {
						t.Fatal(err)
					}
					for {
						frame, err := c.Receive(h.ctx)
						if err != nil {
							t.Fatal(err)
						}
						if frame.ReplyTo == nil || *frame.ReplyTo != request {
							continue
						}
						page, ok := frame.Payload.(protocol.Result)
						if !ok || page.Text == nil || page.ResultID != card.ResultID || page.Revision != card.Revision {
							t.Fatalf("invalid result page: kind=%s error=%+v", frame.Kind, frame.Error)
						}
						full += *page.Text
						next = page.Next
						if next != nil && string(*next) != strconv.Itoa(len(full)) {
							t.Fatal("page cursor skipped or repeated content")
						}
						break
					}
				}
				if full != wantText {
					t.Errorf("%s: full result text lost: got %d bytes, want %d", phase, len(full), len(wantText))
				}
				if card.Completion == nil || card.Completion.State != protocol.CompletionStateClaimed || card.Completion.Label == "" {
					t.Errorf("%s: unverified completion lost: %+v", phase, card.Completion)
				}
			}
			if live.Stream == nil {
				t.Fatal("no live result stream")
			}
			provePager := func() {
				t.Helper()
				if tc.kind != result.DraftType {
					return
				}
				terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=en_US.UTF-8"}, false, true, 100, 30)
				waitComposerReady(t, h, terminal)
				waitScreen(t, h, terminal, design.Text(design.English, "live"))
				waitScreen(t, h, terminal, "Authored result")
				h.keys(terminal, "\x0f\x1b[F") // Open the card and follow its end as pages load.
				waitScreen(t, h, terminal, "RESULT-END")
				h.keys(terminal, "\x04")
				terminal.process.wait(t, 0)
				h.expectTerminalRestored(terminal)
			}
			check("live", live.Stream.Key, live)
			provePager()
			c.Close()
			h.stopCore(core)
			core = start(true)
			c, conversation = conversationClient(t, h)
			topic := "conversation:" + conversation
			request, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})
			if err != nil {
				t.Fatal(err)
			}
			var history protocol.Snapshot
			for {
				frame, err := c.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if frame.ReplyTo != nil && *frame.ReplyTo == request {
					var ok bool
					history, ok = frame.Payload.(protocol.Snapshot)
					if !ok {
						t.Fatalf("expected history snapshot, got %s", frame.Kind)
					}
					break
				}
			}
			count := 0
			for _, row := range history.Items {
				if row.Kind != protocol.KindResult {
					continue
				}
				count++
				var restored protocol.Result
				if err := json.Unmarshal(row.Payload, &restored); err != nil {
					t.Fatal(err)
				}
				check("restored", row.Key, restored)
				// Stream cursors change with Core's epoch; the card must not.
				live.Stream, restored.Stream = nil, nil
				if !reflect.DeepEqual(live, restored) {
					t.Error("restored result differs from live content or metadata")
				}
			}
			provePager()
			if count != 1 || calls.Load() != providerCalls {
				t.Errorf("restored cards=%d, provider calls=%d", count, calls.Load())
			}
			c.Close()
			h.stopCore(core)
		})
	}
}
