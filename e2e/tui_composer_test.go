// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

func TestM1TUIComposer(t *testing.T) {
	data, err := os.ReadFile("m1/tui-composer.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Width, Height       int
		Input, Reply, Draft string
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []struct {
		name, locale, glyphs, prompt, rule string
		language                           design.Language
	}{
		{"unicode-en", "en_US.UTF-8", "unicode", "› ", "─", design.English},
		{"ascii-ko", "ko_KR.UTF-8", "ascii", "> ", "-", design.Korean},
	} {
		t.Run(variant.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct{ Input []json.RawMessage }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(string(mustJSON(t, body.Input)), scenario.Input) {
					t.Error("provider did not receive the submitted input")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", scenario.Reply)
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			}))
			defer provider.Close()
			h, start := installedConversationHarness(t, provider.URL)
			core := start(true)
			terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "COLORTERM=truecolor", "LC_ALL=" + variant.locale, "LINA_GLYPHS=" + variant.glyphs}, false, true, scenario.Width, scenario.Height)
			waitScreen(t, h, terminal, "Ctrl+D")
			h.keys(terminal, scenario.Input+"\r")
			empty := waitScreen(t, h, terminal, scenario.Reply)
			t.Logf("empty screen (%dx%d):\n%s", scenario.Width, scenario.Height, strings.Join(screenRows(empty), "\n"))
			composerY := scenario.Height - 2
			if got := screenRows(empty)[composerY-1]; got != strings.Repeat(variant.rule, scenario.Width) {
				t.Errorf("composer rule = %q, want %d rule cells", got, scenario.Width)
			}
			placeholder, found := design.Lookup(variant.language, "composer.placeholder")
			if !found || screenRows(empty)[composerY] != variant.prompt+placeholder {
				t.Errorf("empty composer = %q, want prompt and catalog placeholder (found=%v)", screenRows(empty)[composerY], found)
			}
			h.keys(terminal, scenario.Draft)
			// Wait for the final cursor update as well as the text; renderer writes
			// may split a frame. The predicate works on both the before and after UI.
			ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
			defer cancel()
			measurer := render.Unicode{}
			draft, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
				row := screenRows(s)[composerY]
				return s.AltScreen && !s.CursorHidden && s.CursorY == composerY &&
					strings.HasSuffix(row, scenario.Draft) && s.CursorX == measurer.Width(row)
			})
			if err != nil {
				t.Fatalf("draft/cursor did not settle: %v", err)
			}
			t.Logf("draft screen; cursor=(%d,%d), zero-based:\n%s", draft.CursorX, draft.CursorY, strings.Join(screenRows(draft), "\n"))
			if got := screenRows(draft)[composerY]; got != variant.prompt+scenario.Draft {
				t.Errorf("draft composer = %q, want prompt and draft", got)
			}
			if want := measurer.Width(variant.prompt) + measurer.Width(scenario.Draft); draft.CursorX != want {
				t.Errorf("Korean cursor column = %d, want %d", draft.CursorX, want)
			}
			if got := screenRows(draft)[composerY+1]; !strings.Contains(got, design.Text(variant.language, "live")) || !strings.Contains(got, "Ctrl+D") {
				t.Errorf("status and hints missing below composer: %q", got)
			}
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			h.stopCore(core)
		})
	}
}
