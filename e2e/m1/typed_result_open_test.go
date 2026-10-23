// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestM1TypedResultOpenInstalledCore(t *testing.T) {
	var calls atomic.Int32
	body := "START-OF-RESULT\n" + strings.Repeat("한글 bounded result content\n", 6000) + "FINAL-CONTENT-READABLE"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			blocks := map[string]any{"blocks": []any{map[string]any{"kind": "paragraph", "text": body}}, "from_summary": nil}
			emitPersonaCall(w, "large-result", "present_result", result.PresentRequest{Type: result.DraftType, Title: "Long document", Summary: "Document ready.", Body: mustJSON(t, blocks)})
			return
		}
		emitResponse(w, "Presented.", true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "results", Version: "1.0"})
	defer c.Close()
	frames := admittedTurn(t, h, c, conversation, "typed-result", "Prepare a long document.")
	var card protocol.Result
	for _, frame := range frames {
		if p, ok := frame.Payload.(protocol.Result); ok && p.Title == "Long document" {
			card = p
		}
	}
	if card.ResultID == "" || card.Text == nil || len(*card.Text) > (64<<10)+200 || strings.Contains(*card.Text, "FINAL-CONTENT-READABLE") {
		t.Fatal("missing bounded typed result preview")
	}
	beforeReads := calls.Load()
	if beforeReads == 0 {
		t.Fatal("result was not produced by a model tool call")
	}
	readAll := func() {
		t.Helper()
		request := protocol.ResultGet{ResultID: card.ResultID, Revision: &card.Revision}
		var full strings.Builder
		pages := 0
		for {
			id, err := c.Result(h.ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			var page protocol.Result
			for {
				frame, err := c.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if frame.ReplyTo == nil || *frame.ReplyTo != id {
					continue
				}
				if frame.Error != nil {
					t.Fatalf("card detail unavailable: %s", frame.Error.Code)
				}
				var ok bool
				page, ok = frame.Payload.(protocol.Result)
				if !ok || page.Text == nil || len(*page.Text) > 64<<10 {
					t.Fatal("missing or oversized result detail")
				}
				break
			}
			full.WriteString(*page.Text)
			pages++
			if page.Next == nil {
				break
			}
			request.Offset = page.Next
		}
		if pages < 3 || strings.TrimSpace(full.String()) != "Document ready.\n"+body {
			t.Fatalf("incomplete result: pages=%d bytes=%d", pages, full.Len())
		}
		t.Logf("read %d complete UTF-8 bytes through %d bounded pages", full.Len(), pages)
	}
	openTUI := func() {
		t.Helper()
		terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=en_US.UTF-8"}, false, true, 100, 32)
		waitScreen(t, h, terminal, "Long document")
		waitScreen(t, h, terminal, design.Text(design.English, "live"))
		h.keys(terminal, "UNSENT-DRAFT")
		h.keys(terminal, "\x0f")
		waitScreen(t, h, terminal, "START-OF-RESULT")
		h.keys(terminal, "\x1b[F")
		waitScreen(t, h, terminal, "FINAL-CONTENT-READABLE")
		h.keys(terminal, "/START-OF-RESULT\r")
		waitScreen(t, h, terminal, "START-OF-RESULT")
		h.keys(terminal, "\x1b")
		waitScreen(t, h, terminal, "UNSENT-DRAFT")
		h.keys(terminal, "\x04")
		terminal.process.wait(t, 0)
		h.expectTerminalRestored(terminal)
	}
	readAll()
	openTUI()
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, _ = conversationClient(t, h, protocol.Capability{Name: "results", Version: "1.0"})
	defer c.Close()
	topic := "conversation:" + conversation
	if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	restored := false
	for !restored {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, ok := frame.Payload.(protocol.Snapshot)
		if !ok {
			continue
		}
		for _, item := range snapshot.Items {
			if item.Kind != protocol.KindResult {
				continue
			}
			var p protocol.Result
			if err := json.Unmarshal(item.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.ResultID == card.ResultID {
				restored = p.Revision == card.Revision && p.TextTruncated != nil && *p.TextTruncated && p.Text != nil && len(*p.Text) < (64<<10)+200
			}
		}
		if !restored {
			t.Fatal("restart lost bounded readable card")
		}
	}
	readAll()
	openTUI()
	if calls.Load() != beforeReads {
		t.Fatalf("reading/restart reran model: %d requests", calls.Load())
	}
}
