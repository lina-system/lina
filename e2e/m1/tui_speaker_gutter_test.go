// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUISpeakerGutterInstalledCore(t *testing.T) {
	for _, mode := range []struct {
		name         string
		ascii, plain bool
	}{
		{name: "truecolor"}, {name: "ascii", ascii: true},
		{name: "no-color", plain: true}, {name: "no-color-ascii", ascii: true, plain: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			// Each mode owns its installed Core, provider, PTY and state root.
			t.Parallel()
			speakerGutterInstalledCore(t, mode.ascii, mode.plain)
		})
	}
}

func speakerGutterInstalledCore(t *testing.T, ascii, plain bool) {
	data, err := os.ReadFile("tui-speaker-gutter.json")
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
		waitPlainComposerReady(t, h, terminal)
	} else {
		waitComposerReady(t, h, terminal)
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
		seenReply := false
		for {
			frame := tap.next(t, h)
			if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text == exchange.Reply {
				seenReply = true
			}
			if state, ok := frame.Payload.(protocol.TurnState); ok && seenReply && state.Active != nil && !*state.Active {
				break
			}
		}
		waitScreen(t, h, terminal, exchange.Reply)
	}
	// Editing the composer is a visible redraw barrier after both replies.
	h.keys(terminal, "capture-ready")
	snapshot := waitScreen(t, h, terminal, "capture-ready")
	if !plain {
		// The tap sees Core's event before the TUI necessarily renders it.
		// Capture only after every reply's completion metadata is on screen.
		worked := strings.TrimSuffix(design.Text(design.English, "activity.worked_for"), "%s")
		snapshot, err = terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
			return strings.Contains(screenText(s), "capture-ready") && strings.Count(screenText(s), worked) == len(scenario.Exchanges)
		})
		if err != nil {
			t.Fatalf("completion metadata did not render: %v; cells=%s", err, screenText(snapshot))
		}
	}
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
			for _, row := range []string{"> You: " + exchange.Input, "* Lina: " + exchange.Reply} {
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
						prefix = "* Lina: "
					}
				}
				if rows[found] != prefix+text {
					t.Errorf("speaker row = %q, want %q", rows[found], prefix+text)
				}
				if previous > 0 && (found != previous+2 || rows[found-1] != "") {
					t.Errorf("turn rows %d -> %d do not have exactly one empty separator", previous, found)
				}
				previous = found
				if i == 1 {
					// The completion duration belongs to this turn. Preserve the single
					// empty separator after its last row, and require the new row itself.
					duration := strings.ReplaceAll(regexp.QuoteMeta(design.Text(design.English, "activity.worked_for")), "%s", `[0-9]+(?:s|m [0-9]{2}s|h [0-9]{2}m [0-9]{2}s)`)
					if found+1 >= len(rows) || !regexp.MustCompile("^"+duration+"$").MatchString(rows[found+1]) {
						t.Fatalf("assistant turn has no valid completion duration after row %d", found)
					}
					previous++
				}
				glyph, body := snapshot.Cells[found][0], snapshot.Cells[found][utf8.RuneCountInString(prefix)]
				if glyph.Foreground == nil || body.Foreground == nil || reflect.DeepEqual(glyph.Foreground, body.Foreground) {
					t.Errorf("speaker/body foreground not distinct: %v / %v", glyph.Foreground, body.Foreground)
				}
				t.Logf("speaker row %d: mark foreground=%v body foreground=%v", found, glyph.Foreground, body.Foreground)
				if i == 1 {
					// Completion metadata belongs to the reply; the turn gap follows it.
					worked := strings.TrimSuffix(design.Text(design.English, "activity.worked_for"), "%s")
					if found+1 >= len(rows) || !strings.HasPrefix(rows[found+1], worked) {
						t.Fatal("reply lost its completion duration")
					}
					duration := strings.TrimPrefix(rows[found+1], worked)
					if elapsed, err := time.ParseDuration(strings.ReplaceAll(duration, " ", "")); err != nil || elapsed < 0 {
						t.Fatalf("invalid completion duration %q", duration)
					}
					completion := snapshot.Cells[found+1][0]
					if completion.Foreground == nil || reflect.DeepEqual(completion.Foreground, body.Foreground) {
						t.Error("completion duration lost its muted foreground")
					}
					previous = found + 1
				}
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
