// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/tui/render"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUINewline(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("tui-newline.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Width, Height      int
		Seed, Ready, Reply string
		Lines              []string
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []struct{ name, reply, newline, hint, locale string }{
		{"legacy", "", "\x1b\r", "Alt+Enter", "en_US.UTF-8"},
		{"disambiguated", "\x1b[?1u", "\x1b[13;2u", "Shift+Enter", "en_US.UTF-8"},
		{"event-types-ko", "\x1b[?3u", "\x1b[13;2:1u\x1b[13;2:3u", "Shift+Enter", "ko_KR.UTF-8"},
	} {
		t.Run(variant.name, func(t *testing.T) {
			want := strings.Join(scenario.Lines, "\n")
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				input, reply := scenario.Seed, scenario.Ready
				if calls.Add(1) > 1 {
					input, reply = want, scenario.Reply
				}
				var body struct{ Input []json.RawMessage }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(string(mustJSON(t, body.Input)), string(mustJSON(t, input))) {
					t.Errorf("provider did not receive the complete input %q", input)
				}
				emitResponse(w, reply, true)
			}))
			defer provider.Close()
			h, start := installedConversationHarness(t, provider.URL)
			core := start(true)
			client, conversation := conversationClient(t, h)
			admittedTurn(t, h, client, conversation, "newline-ready", scenario.Seed)
			terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "COLORTERM=truecolor", "LC_ALL=" + variant.locale}, false, true, scenario.Width, scenario.Height)
			// Restored history proves the installed TUI has discovered its
			// conversation, independent of footer wording and startup timing.
			waitScreen(t, h, terminal, scenario.Ready)
			h.keys(terminal, variant.reply)
			paste := func(text string) string { return "\x1b[200~" + text + "\x1b[201~" }
			h.keys(terminal, paste(scenario.Lines[0])+variant.newline+paste(scenario.Lines[1])+"\x1b\r"+paste(scenario.Lines[2])+"\n"+paste(scenario.Lines[3]))
			// Text and the final hardware cursor provide a processing barrier;
			// no sleep or absence-of-output timeout is used to prove no send.
			ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
			defer cancel()
			snapshot, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
				rows := screenRows(s)
				return strings.HasSuffix(rows[scenario.Height-2], scenario.Lines[3]) && !s.CursorHidden &&
					s.CursorY == scenario.Height-2 && s.CursorX == (render.Unicode{}).Width(rows[scenario.Height-2])
			})
			if err != nil {
				t.Fatalf("draft/cursor did not settle: %v; cells=%s", err, screenText(snapshot))
			}
			rows := screenRows(snapshot)
			for i, line := range scenario.Lines {
				if got := rows[scenario.Height-1-len(scenario.Lines)+i]; !strings.HasSuffix(got, line) {
					t.Fatalf("composer row %d = %q, want separate line %q; cells=%s", i, got, line, screenText(snapshot))
				}
			}
			if calls.Load() != 1 {
				t.Fatal("newline submitted an incomplete draft")
			}
			h.keys(terminal, "\r")
			waitScreen(t, h, terminal, scenario.Reply)
			for {
				frame, err := client.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
					break
				}
			}
			snapshot = waitScreen(t, h, terminal, variant.hint)
			if !strings.Contains(screenRows(snapshot)[scenario.Height-2], variant.hint) {
				t.Fatal("empty composer did not advertise the supported newline key")
			}
			topic := "conversation:" + conversation
			if _, err := client.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			frame, err := client.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			history, ok := frame.Payload.(protocol.Snapshot)
			if !ok {
				t.Fatalf("expected durable history, got %s", frame.Kind)
			}
			var inputs []string
			for _, row := range history.Items {
				if row.Kind != protocol.KindItemDone {
					continue
				}
				var item protocol.ItemDone
				if err := json.Unmarshal(row.Payload, &item); err != nil {
					t.Fatal(err)
				}
				if item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerPerson && item.Text != nil {
					inputs = append(inputs, *item.Text)
				}
			}
			if len(inputs) != 2 || inputs[0] != scenario.Seed || inputs[1] != want || calls.Load() != 2 {
				t.Fatalf("Enter did not admit exactly one complete draft: inputs=%q calls=%d", inputs, calls.Load())
			}
			t.Logf("newline=%s; durable input=%q; provider calls=%d (including readiness seed)", variant.hint, inputs[1], calls.Load())
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			client.Close()
			h.stopCore(core)
		})
	}
}
