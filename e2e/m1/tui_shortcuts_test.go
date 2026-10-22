// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIShortcuts(t *testing.T) {
	t.Parallel()

	for _, variant := range []struct {
		name, locale, glyphs, mark string
		language                   design.Language
	}{
		{"en-unicode", "en_US.UTF-8", "unicode", "●", design.English},
		{"ko-ascii", "ko_KR.UTF-8", "ascii", "*", design.Korean},
	} {
		for _, width := range []int{100, 60, 40} {
			t.Run(fmt.Sprintf("%s/%d", variant.name, width), func(t *testing.T) {
				var calls atomic.Int32
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct{ Input []json.RawMessage }
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					reply, input := "SHORTCUT-READY", "shortcut fixture"
					if calls.Add(1) == 2 {
						reply, input = "QUESTION-RECEIVED", "why?"
					}
					if !strings.Contains(string(mustJSON(t, body.Input)), input) {
						t.Errorf("provider missing submitted input %q", input)
					}
					emitResponse(w, reply, true)
				}))
				defer provider.Close()
				h, start := installedConversationHarness(t, provider.URL)
				core := start(true)
				client, conversation := conversationClient(t, h)
				defer client.Close()
				input, id := "shortcut fixture", "shortcut-ready"
				if _, err := client.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &id, Text: &input}); err != nil {
					t.Fatal(err)
				}
				for {
					frame, err := client.Receive(h.ctx)
					if err != nil {
						t.Fatal(err)
					}
					if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
						break
					}
				}
				term := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=" + variant.locale, "LINA_GLYPHS=" + variant.glyphs}, false, true, width, 34)
				snapshot := waitScreen(t, h, term, "SHORTCUT-READY")
				footer := screenRows(snapshot)[33]
				// This short transcript does not need a scroll hint. The remaining
				// complete semantic hint units retain their priority order.
				var expected []string
				for _, hint := range [][2]string{{"?", "shortcuts.title"}, {"Esc", "shortcuts.stop"}, {"Enter", "shortcuts.send"}, {"Ctrl+D", "action.quit"}, {"Alt+Enter", "shortcuts.newline"}, {"Ctrl+O", "shortcuts.open_hint"}, {"F6", "shortcuts.decisions"}} {
					expected = append(expected, hint[0]+" "+design.Text(variant.language, hint[1]))
				}
				prefix := variant.mark + " " + design.Text(variant.language, "live")
				if !strings.HasPrefix(footer, prefix+"  "+expected[0]) {
					t.Fatalf("connection/highest-priority hint missing at %d: %q", width, footer)
				}
				remainder := strings.TrimPrefix(footer, prefix)
				for _, hint := range expected {
					if remainder == "" {
						break
					}
					if !strings.HasPrefix(remainder, "  "+hint) {
						t.Fatalf("partial or out-of-order hint at %d: %q", width, remainder)
					}
					remainder = strings.TrimPrefix(remainder, "  "+hint)
				}
				if remainder != "" {
					t.Fatalf("unexpected partial hint: %q", remainder)
				}
				wait := func(predicate func(tuiterm.Snapshot) bool) tuiterm.Snapshot {
					t.Helper()
					ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
					defer cancel()
					snapshot, err := term.screen.Wait(ctx, predicate)
					if err != nil {
						t.Fatalf("shortcut screen: %v\n%s", err, screenText(snapshot))
					}
					return snapshot
				}
				for _, open := range []string{"?", "/help\r"} {
					h.keys(term, open)
					sheet := wait(func(s tuiterm.Snapshot) bool {
						text := screenText(s)
						for _, key := range []string{"Esc", "Alt+Enter", "PgUp/PgDn", "Ctrl+O", "F6"} {
							if !strings.Contains(text, key) {
								return false
							}
						}
						return s.CursorHidden
					})
					if !strings.Contains(screenRows(sheet)[33], design.Text(variant.language, "live")) {
						t.Fatal("sheet dropped connection word")
					}
					h.keys(term, "\x1b")
					wait(func(s tuiterm.Snapshot) bool {
						return !s.CursorHidden && strings.Contains(screenText(s), "SHORTCUT-READY")
					})
				}
				h.keys(term, "why?")
				wait(func(s tuiterm.Snapshot) bool { return !s.CursorHidden && strings.HasSuffix(screenRows(s)[32], "why?") })
				h.keys(term, "\r")
				wait(func(s tuiterm.Snapshot) bool { return strings.Contains(screenText(s), "QUESTION-RECEIVED") })
				if calls.Load() != 2 {
					t.Fatalf("help submitted input or replayed a request: calls=%d", calls.Load())
				}
				t.Logf("%d columns: %s; both sheets and draft question verified through installed Core", width, footer)
				h.keys(term, "\x04")
				term.process.wait(t, 0)
				h.expectTerminalRestored(term)
				h.stopCore(core)
			})
		}
	}
}
