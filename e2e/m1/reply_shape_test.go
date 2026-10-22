// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/layout"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
	"github.com/thisisjun786/lina/kit/secret"
)

func TestM1ReplyShapeLONGQA(t *testing.T) {
	t.Parallel()

	for _, language := range []design.Language{design.English, design.Korean} {
		t.Run(string(language), func(t *testing.T) {
			testReplyShapeLONGQA(t, language)
		})
	}
}

func testReplyShapeLONGQA(t *testing.T, language design.Language) {
	var scenario struct {
		Input, Line, First, End, Followup, Reply string
		Lines                                    int
	}
	data, err := os.ReadFile("reply-shape.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for i := 1; i <= scenario.Lines; i++ {
		lines = append(lines, fmt.Sprintf(scenario.Line, i))
	}
	lines[0] = scenario.First + " " + lines[0]
	lines[len(lines)-1] += " " + scenario.End
	full := strings.Join(lines, "\n")
	want := strings.Join(lines[:5], "\n")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) > 1 {
			// The next request retains exactly one full assistant source, without
			// feeding the generated card's storage JSON back as another reply.
			var copies int
			for _, raw := range request.Input {
				var item struct{ Role, Content string }
				if json.Unmarshal(raw, &item) == nil && item.Role == "assistant" && strings.Contains(item.Content, scenario.End) {
					copies++
					if item.Content != full {
						t.Error("card storage leaked into model history")
					}
				}
			}
			if copies != 1 {
				t.Errorf("full reply in model history: got %d copies", copies)
			}
			emitResponse(w, scenario.Reply, true)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, delta := range lines {
			fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", delta+"\n")
			w.(http.Flusher).Flush()
		}
		emitResponse(w, full, true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	environment := []string{"TERM=xterm-256color", "LC_ALL=" + string(language)}
	screenTUI, _ := h.startConversationTUI(100, 34, environment...)
	waitScreen(t, h, screenTUI, design.Text(language, "live"))
	h.keys(screenTUI, scenario.Input)
	waitScreen(t, h, screenTUI, scenario.Input)
	h.keys(screenTUI, "\r")
	var visible string
	var card protocol.Result
	for {
		e, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch p := e.Payload.(type) {
		case protocol.OutputDelta:
			if p.Text != nil {
				visible += *p.Text
			}
		case protocol.ItemDone:
			if p.Speaker != nil && *p.Speaker == protocol.ConversationSpeakerAgent && p.Text != nil {
				visible = *p.Text
			}
		case protocol.Result:
			card, visible = p, p.Summary
		}
		if strings.Count(strings.TrimRight(visible, "\n"), "\n") >= 5 || strings.Contains(visible, scenario.End) {
			t.Fatalf("LONG-QA escaped the inline ceiling: %d lines; tail=%t", strings.Count(visible, "\n")+1, strings.Contains(visible, scenario.End))
		}
		if p, ok := e.Payload.(protocol.TurnState); ok && p.Active != nil && !*p.Active {
			break
		}
	}
	if card.Type != protocol.ResultTypeDocumentDraft || card.Status != protocol.ResultStatusPreview || card.Text == nil || *card.Text != full || card.Summary != want {
		t.Fatalf("missing complete draft with sentence-bounded summary: type=%s status=%s text=%v summary=%q", card.Type, card.Status, card.Text, card.Summary)
	}
	checkScreen := func(terminal *terminal) {
		// Inspect parsed cells from the installed TUI, never raw escape bytes.
		plan := layout.Plan(layout.Size{Width: 100, Height: 34}, layout.Caps{SidePane: true}, layout.Shape{ComposerRows: 1})
		region := plan.Conversation
		affordance := design.Text(language, "reply.open_pager")
		conversationRows := func(snapshot tuiterm.Snapshot) []string {
			return screenRows(snapshot)[region.Y : region.Y+region.Height]
		}
		wait := func(predicate func(tuiterm.Snapshot) bool) tuiterm.Snapshot {
			t.Helper()
			ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
			defer cancel()
			snapshot, err := terminal.screen.Wait(ctx, predicate)
			if err != nil {
				t.Fatalf("LONG-QA conversation assertion: %v\n%s", err, strings.Join(screenRows(snapshot), "\n"))
			}
			return snapshot
		}
		latest := wait(func(s tuiterm.Snapshot) bool {
			return s.AltScreen && !s.CursorHidden && s.CursorY == plan.Composer.Y &&
				strings.Contains(strings.Join(conversationRows(s), "\n"), scenario.End) && strings.Contains(screenText(s), affordance)
		})
		t.Logf("LONG-QA latest (100x34):\n%s", strings.Join(screenRows(latest), "\n"))
		if !latest.AltScreen || latest.CursorHidden || latest.CursorY != plan.Composer.Y {
			t.Fatal("latest reply is outside the conversation")
		}
		// Stitch overlapping PgUp views into the complete transcript, so a
		// repeated summary or body cannot hide outside the current viewport.
		transcript := conversationRows(latest)
		page := latest
		for pages := 0; !strings.Contains(strings.Join(conversationRows(page), "\n"), scenario.Input); pages++ {
			if pages >= scenario.Lines {
				t.Fatal("PgUp never reached the first turn")
			}
			previous := strings.Join(conversationRows(page), "\n")
			h.keys(terminal, "\x1b[5~")
			page = wait(func(s tuiterm.Snapshot) bool {
				return s.AltScreen && !s.CursorHidden && s.CursorY == plan.Composer.Y &&
					strings.Join(conversationRows(s), "\n") != previous
			})
			rows := conversationRows(page)
			overlap := min(len(rows), len(transcript))
			for overlap > 0 && strings.Join(rows[len(rows)-overlap:], "\n") != strings.Join(transcript[:overlap], "\n") {
				overlap--
			}
			if overlap == 0 {
				t.Fatal("PgUp skipped conversation rows")
			}
			transcript = append(append([]string{}, rows[:len(rows)-overlap]...), transcript...)
		}
		t.Logf("LONG-QA first page (100x34):\n%s", strings.Join(screenRows(page), "\n"))
		if !strings.Contains(strings.Join(conversationRows(page), "\n"), scenario.First) || page.CursorHidden || page.CursorY != plan.Composer.Y {
			t.Fatal("PgUp did not reach the first marker inside the conversation")
		}
		complete := strings.Join(transcript, "\n")
		for _, line := range lines {
			if copies := strings.Count(complete, line); copies != 1 {
				t.Fatalf("reply line %q appears %d times, want exactly once", line, copies)
			}
		}
		if strings.Count(complete, affordance) != 1 || strings.Index(complete, scenario.End) > strings.Index(complete, affordance) {
			t.Fatal("expected one pager affordance after the complete reply")
		}
		h.keys(terminal, "\x0f")
		wait(func(s tuiterm.Snapshot) bool {
			return s.AltScreen && s.CursorHidden && len(s.Cells) == 34 &&
				strings.Contains(screenRows(s)[0], scenario.First)
		})
		h.keys(terminal, "\x1b[F")
		wait(func(s tuiterm.Snapshot) bool {
			text := strings.Join(screenRows(s)[:33], "\n")
			return s.AltScreen && s.CursorHidden && strings.Contains(text, scenario.End) && !strings.Contains(text, scenario.First)
		})
		h.keys(terminal, "\x1b")
		wait(func(s tuiterm.Snapshot) bool {
			return s.AltScreen && !s.CursorHidden && s.CursorY == plan.Composer.Y &&
				strings.Contains(strings.Join(conversationRows(s), "\n"), scenario.First)
		})
		h.keys(terminal, "\x04")
		terminal.process.wait(t, 0)
		h.expectTerminalRestored(terminal)
	}
	checkScreen(screenTUI)
	c.Close()
	h.stopCore(core)
	// Read the actual installed ledger after shutdown, not a projection that
	// could accidentally substitute the truncated presentation for evidence.
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var source string
	if err = db.QueryRow(`SELECT body FROM conv_event WHERE speaker_kind='agent' AND scaffold_kind IS NULL`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != full {
		t.Fatal("ledger did not retain the complete source")
	}
	var agentEvents int
	if err = db.QueryRow(`SELECT count(*) FROM conv_event WHERE speaker_kind='agent'`).Scan(&agentEvents); err != nil {
		t.Fatal(err)
	}
	if agentEvents != 1 {
		t.Errorf("long reply recorded %d agent events; want exactly one", agentEvents)
	}
	var cardBody string
	if err = db.QueryRow(`SELECT body FROM conv_event WHERE scaffold_kind='result'`).Scan(&cardBody); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cardBody, scenario.End) {
		t.Error("full-reply card duplicates the source body instead of referencing it")
	}
	var cards int
	if err = db.QueryRow(`SELECT count(*) FROM conv_event c JOIN ev_rel r ON r.event_id=c.id WHERE c.scaffold_kind='result' AND r.kind='view_of'`).Scan(&cards); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if cards != 1 {
		t.Fatalf("durable linked cards = %d, want 1", cards)
	}
	// Recall reads the installed ledger, including its search indexes.
	func() {
		ctl, err := ctlfiles.Open(h.root)
		if err != nil {
			t.Fatal(err)
		}
		defer ctl.Close()
		if err := ctl.Acquire(); err != nil {
			t.Fatal(err)
		}
		epoch, err := ctl.Epoch()
		if err != nil {
			t.Fatal(err)
		}
		canonDB, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon/canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
		if err != nil {
			t.Fatal(err)
		}
		defer canonDB.Close(h.ctx)
		hits, err := ledger.New(canonDB, nil).Search(h.ctx, ledger.SearchQuery{AgentID: c.Welcome.Agents[0].AgentID, PersonID: c.Welcome.PersonID, Text: scenario.End})
		if err != nil || len(hits) != 1 || hits[0].Body != full {
			t.Fatalf("installed recall lost or duplicated full reply: %d hits, %v", len(hits), err)
		}
	}()
	core = start(true)
	c, conversation = conversationClient(t, h)
	again := h.startTerminalSize(environment, false, true, 100, 34)
	checkScreen(again)
	admittedTurn(t, h, c, conversation, "followup", scenario.Followup)
	c.Close()
	h.stopCore(core)
	if calls.Load() != 2 {
		t.Fatalf("restart replayed the model call: %d", calls.Load())
	}
}

func TestM1ReplyShapeFinalItemCeilings(t *testing.T) {
	t.Parallel()

	five := "하나.\n둘.\n셋.\n넷.\n다섯."
	cases := []struct {
		name, full, visible string
		card                bool
	}{
		{"at-lines", five, five, false},
		{"over-lines", five + "\n여섯.", five, true},
		{"at-chars", "OK! " + strings.Repeat("x", 297), "OK! " + strings.Repeat("x", 297), false},
		{"over-chars", "OK! " + strings.Repeat("x", 298), "OK!", true},
		{"wrapped-korean", "끝. " + strings.Repeat("가", 198), "끝.", true},
		{"no-sentence", strings.Repeat("가", 301), "", true},
		{"danda-sentence", "यह पूरा है। " + strings.Repeat("क", 301), "यह पूरा है।", true},
		{"urdu-sentence", "یہ مکمل ہے۔ " + strings.Repeat("ک", 301), "یہ مکمل ہے۔", true},
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(calls.Add(1)) - 1
		if i >= len(cases) {
			t.Error("unexpected model call")
			return
		}
		// Providers may deliver only a final item. The same ceiling still applies.
		emitResponse(w, cases[i].full, true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := admittedTurn(t, h, c, conversation, tc.name, tc.name)
			found := 0
			for _, frame := range frames {
				switch p := frame.Payload.(type) {
				case protocol.ItemDone:
					if p.Speaker != nil && *p.Speaker == protocol.ConversationSpeakerAgent {
						found++
						if tc.card || p.Text == nil || *p.Text != tc.visible {
							t.Error("final item bypassed the reply ceiling")
						}
					}
				case protocol.Result:
					found++
					if !tc.card || p.Summary != tc.visible || p.Text == nil || *p.Text != tc.full {
						t.Error("card lost the bounded summary or complete reply")
					}
				}
			}
			if found != 1 {
				t.Errorf("final reply projections = %d, want 1", found)
			}
		})
	}
	c.Close()
	h.stopCore(core)
}

func TestM1ReplyShapeCredentialMasking(t *testing.T) {
	t.Parallel()

	credential := "sk-" + strings.Repeat("a", 24)
	cases := []struct {
		name   string
		deltas []string
	}{
		{"inline", []string{"Credential: " + credential + " safe. "}},
		{"card", []string{strings.Repeat("Public sentence. ", 30), credential + " safe. "}},
		{"split", []string{"Credential: sk-", strings.Repeat("a", 12), strings.Repeat("a", 12) + " safe. "}},
		{"quoted", []string{"password=\"synthetic ", "private value\" safe. "}},
		{"private-key", []string{"-----BEGIN PRIVATE ", "KEY-----\nsynthetic-private-body\n", "-----END PRIVATE KEY-----\n"}},
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc := cases[int(calls.Add(1))-1]
		w.Header().Set("Content-Type", "text/event-stream")
		for _, delta := range tc.deltas {
			data, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": delta})
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		emitResponse(w, strings.Join(tc.deltas, ""), true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	defer c.Close()
	defer h.stopCore(core)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := admittedTurn(t, h, c, conversation, tc.name, tc.name)
			var streamed, final string
			for _, frame := range frames {
				switch p := frame.Payload.(type) {
				case protocol.OutputDelta:
					if p.Text != nil {
						streamed += *p.Text
					}
				case protocol.ItemDone:
					if p.Speaker != nil && *p.Speaker == protocol.ConversationSpeakerAgent && p.Text != nil {
						final = *p.Text
					}
				case protocol.Result:
					if p.Text != nil {
						final = *p.Text
					}
					streamed += p.Summary
				}
				data, _ := json.Marshal(frame)
				for _, forbidden := range []string{credential, strings.Repeat("a", 12), "synthetic", "private value"} {
					if strings.Contains(string(data), forbidden) {
						t.Errorf("client frame exposed credential bytes (%s)", frame.Kind)
					}
				}
			}
			masked, err := secrets.Mask(strings.Join(tc.deltas, ""))
			if err != nil {
				t.Fatal(err)
			}
			if final != masked.Text() || !strings.Contains(final, secret.Placeholder) {
				t.Error("final reply does not match ledger masking")
			}
			if strings.Contains(streamed, credential) {
				t.Error("streamed deltas reconstruct unmasked credential")
			}
		})
	}
}

func TestM1ReplyShapeLargeFrames(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, full string }{
		{"over-one-MiB", "Start. " + strings.Repeat("large text ", (1<<20)/10+1)},
		{"escaped-json", "Start. " + strings.Repeat("<한>\n", 180000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				// A single large delta and the final item must both fit client transport.
				data, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": tc.full})
				fmt.Fprintf(w, "data: %s\n\n", data)
				w.(http.Flusher).Flush()
				emitResponse(w, tc.full, true)
			}))
			defer server.Close()
			h, start := installedConversationHarness(t, server.URL)
			core := start(true)
			c, conversation := conversationClient(t, h)
			frames := admittedTurn(t, h, c, conversation, tc.name, "large reply")
			cards := 0
			for _, frame := range frames {
				data, err := json.Marshal(frame)
				if err != nil || len(data) >= rpc.MaxFrameBytes {
					t.Errorf("oversized client frame: %d, %v", len(data), err)
				}
				if p, ok := frame.Payload.(protocol.Result); ok {
					cards++
					if p.Text == nil || !strings.HasPrefix(*p.Text, "Start.") || len(*p.Text) >= len(tc.full) {
						t.Error("missing bounded preview")
					}
				}
			}
			if cards < 2 {
				t.Errorf("got %d cards, want streamed and final", cards)
			}
			c.Close()
			h.stopCore(core)
			db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			var material string
			var revision int
			err = db.QueryRow(`SELECT material_id, material_revision FROM conv_event WHERE speaker_kind='agent' AND scaffold_kind IS NULL`).Scan(&material, &revision)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(h.root, "materials", "lina", material, fmt.Sprint(revision), "content"))
			if err != nil || string(body) != tc.full {
				t.Fatalf("complete reply material lost: %v", err)
			}
			// Restored history must also be deliverable on the same bounded transport.
			core = start(true)
			c, _ = conversationClient(t, h)
			c.Close()
			h.stopCore(core)
		})
	}
}
