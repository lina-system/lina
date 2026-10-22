// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestM1TUISpeakerGutterInstalledCore(t *testing.T) {
	for _, mode := range []struct {
		name         string
		ascii, plain bool
	}{
		{name: "truecolor"}, {name: "ascii", ascii: true},
		{name: "no-color", plain: true}, {name: "no-color-ascii", ascii: true, plain: true},
	} {
		t.Run(mode.name, func(t *testing.T) { speakerGutterInstalledCore(t, mode.ascii, mode.plain) })
	}
}

func speakerGutterInstalledCore(t *testing.T, ascii, plain bool) {
	data, err := os.ReadFile("m1/tui-speaker-gutter.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Width, Height int
		Exchanges     []struct{ Input, Reply string }
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := int(calls.Add(1)) - 1
		if call >= len(scenario.Exchanges) {
			t.Error("unexpected provider call")
			http.Error(w, "unexpected call", 500)
			return
		}
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(mustJSON(t, body.Input)), scenario.Exchanges[call].Input) {
			t.Error("typed input missing from provider request")
		}
		emitResponse(w, scenario.Exchanges[call].Reply, true)
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	environment := []string{"TERM=xterm-256color", "COLORTERM=truecolor", "LINA_THEME=dark"}
	if ascii {
		environment = append(environment, "LINA_GLYPHS=ascii")
	}
	if plain {
		environment = append(environment, "NO_COLOR=1")
	}
	socket := h.socket
	tap := newAccountTap(t, h, socket, true)
	h.socket = tap.socket
	terminal := h.startTerminalSize(environment, false, true, scenario.Width, scenario.Height)
	h.socket = socket
	if plain {
		// The shared color-composer barrier now requires alternate screen.
		// Plain output must remain on the normal screen, with the same live
		// status and composer hints; history discovery is checked below too.
		if _, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
			text := screenText(s)
			return !s.AltScreen && strings.Contains(text, "Ctrl+D") && strings.Contains(text, design.Text(design.English, "live")+"  ")
		}); err != nil {
			t.Fatal("plain composer not ready", err)
		}
	} else {
		waitScreen(t, h, terminal, "Ctrl+D")
	}
	// The composer is visible before its conversation destination is known.
	// The subscription proves History was consumed; do not race initial discovery.
	for {
		frame := tap.next(t, h)
		if snapshot, ok := frame.Payload.(protocol.Snapshot); ok && strings.HasPrefix(snapshot.Topic, "conversation:") {
			break
		}
	}
	for _, exchange := range scenario.Exchanges {
		h.keys(terminal, exchange.Input+"\r")
		for {
			frame := tap.next(t, h)
			if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
				break
			}
		}
		waitScreen(t, h, terminal, exchange.Reply)
	}
	// Editing the composer is a visible redraw barrier after both replies.
	h.keys(terminal, "capture-ready")
	snapshot := waitScreen(t, h, terminal, "capture-ready")
	var rows []string
	for _, row := range snapshot.Cells {
		var line strings.Builder
		for _, cell := range row {
			line.WriteString(cell.Content)
		}
		rows = append(rows, strings.TrimRight(line.String(), " "))
	}
	t.Logf("100x34 parsed cells (alternate screen=%t):\n%s", snapshot.AltScreen, strings.Join(rows, "\n"))
	if plain {
		raw := string(terminal.screen.Bytes())
		if strings.Contains(raw, "\x1b") {
			t.Error("NO_COLOR emitted terminal escapes")
		}
		for _, exchange := range scenario.Exchanges {
			for _, row := range []string{"> You: " + exchange.Input, "* LINA: " + exchange.Reply} {
				if !strings.Contains(raw, row) {
					t.Errorf("plain speaker missing: %q", row)
				}
			}
		}
		if snapshot.AltScreen {
			t.Error("NO_COLOR entered alternate screen")
		}
	} else {
		previous := 0
		for _, exchange := range scenario.Exchanges {
			for i, text := range []string{exchange.Input, exchange.Reply} {
				found := -1
				for y, row := range rows {
					if strings.Contains(row, text) {
						found = y
						break
					}
				}
				if found < 0 {
					t.Errorf("missing message %q", text)
					continue
				}
				prefix := "› "
				if i == 1 {
					prefix = "● "
				}
				if ascii {
					prefix = "> You: "
					if i == 1 {
						prefix = "* LINA: "
					}
				}
				if rows[found] != prefix+text {
					t.Errorf("speaker row = %q, want %q", rows[found], prefix+text)
				}
				if previous > 0 && (found != previous+2 || rows[found-1] != "") {
					t.Errorf("turn rows %d -> %d do not have exactly one empty separator", previous, found)
				}
				previous = found
				glyph, body := snapshot.Cells[found][0], snapshot.Cells[found][utf8.RuneCountInString(prefix)]
				if glyph.Foreground == nil || body.Foreground == nil || reflect.DeepEqual(glyph.Foreground, body.Foreground) {
					t.Errorf("speaker/body foreground not distinct: %v / %v", glyph.Foreground, body.Foreground)
				}
				t.Logf("speaker row %d: mark foreground=%v body foreground=%v", found, glyph.Foreground, body.Foreground)
			}
		}
		if !snapshot.AltScreen || !snapshot.Cells[0][0].Bold {
			t.Error("conversation lost alternate screen or styled header")
		}
	}
	if calls.Load() != int32(len(scenario.Exchanges)) {
		t.Error("missing or duplicate model exchange")
	}
	h.keys(terminal, "\x03\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)
}
