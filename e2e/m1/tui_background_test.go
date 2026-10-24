// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

// Real PTY output must paint every cell even when the terminal's default
// background is light and it supplies no COLORFGBG hint.
func TestM1TUIBackgroundInstalledCore(t *testing.T) {
	for _, profile := range []string{"xterm", "truecolor"} {
		t.Run(profile, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("typing a draft must not call the provider")
				http.Error(w, "unexpected request", http.StatusBadRequest)
			}))
			defer provider.Close()
			h, start := installedConversationHarness(t, provider.URL)
			core := start(true)
			env := []string{"TERM=xterm", "COLORFGBG=", "LINA_THEME="}
			if profile == "truecolor" {
				env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
			}
			terminal, _ := h.startConversationTUI(80, 24, env...)
			defer func() {
				h.keys(terminal, "\x04")
				terminal.process.wait(t, 0)
				h.expectTerminalRestored(terminal)
				h.stopCore(core)
			}()
			assertTerminalBackground(t, waitComposerReady(t, h, terminal))
			h.keys(terminal, "Background fixture")
			assertTerminalBackground(t, waitScreen(t, h, terminal, "Background fixture"))
		})
	}
}

func assertTerminalBackground(t *testing.T, snapshot tuiterm.Snapshot) {
	t.Helper()
	want := snapshot.Cells[0][0].Background
	if want == nil {
		t.Fatal("header has no explicit background")
	}
	empty, text := false, false
	for y, row := range snapshot.Cells {
		var line strings.Builder
		for x, cell := range row {
			line.WriteString(cell.Content)
			if !reflect.DeepEqual(cell.Background, want) {
				t.Fatalf("cell (%d,%d) %q background = %v, want %v", x, y, cell.Content, cell.Background, want)
			}
		}
		if strings.TrimSpace(line.String()) == "" {
			empty = true
		} else {
			text = true
		}
	}
	if !empty || !text {
		t.Fatalf("fixture must exercise empty and text rows: empty=%t text=%t", empty, text)
	}
}
