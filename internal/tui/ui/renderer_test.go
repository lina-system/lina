// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

func TestRendererLateInitialSizeCannotClipCurrentView(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	output, err := tuiterm.NewRecorder(81, 60)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	renderer := startTerminalRenderer(ctx, rawTerminalWriter{output}, 80, 24)
	defer renderer.close()
	frame := func(marker string) tea.View {
		rows := make([]string, 60)
		rows[0] = "header"
		rows[58] = marker
		rows[59] = "status"
		v := tea.NewView(strings.Join(rows, "\n"))
		v.AltScreen = true
		v.Cursor = tea.NewCursor(len(marker), 58)
		return v
	}
	size := tea.WindowSizeMsg{Width: 81, Height: 60}
	if err = renderer.show(frame("initial"), size); err != nil {
		t.Fatal(err)
	}
	// Bubble Tea dispatches its initial WindowSizeMsg from a goroutine. Force
	// the late delivery observed on a fresh TUI, after the owning input decoder
	// has already supplied the actual dimensions. No scheduling lottery is used.
	renderer.program.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	if err = renderer.show(frame("current"), size); err != nil {
		t.Fatal(err)
	}
	snapshot, err := output.Wait(ctx, func(s tuiterm.Snapshot) bool {
		var bottom strings.Builder
		for _, cell := range s.Cells[58] {
			bottom.WriteString(cell.Content)
		}
		return strings.Contains(bottom.String(), "current") && s.CursorY == 58 && !s.CursorHidden && s.AltScreen
	})
	if err != nil {
		t.Fatalf("late initial dimensions clipped current composer: %v; cursor=(%d,%d)", err, snapshot.CursorX, snapshot.CursorY)
	}
}

func TestRendererCancellationRestoresWheelModes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output, err := tuiterm.NewRecorder(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	renderer := startTerminalRenderer(ctx, rawTerminalWriter{output}, 80, 24)
	defer renderer.close()
	view := tea.NewView("mode-cleanup")
	view.AltScreen = true
	if err := renderer.show(view, tea.WindowSizeMsg{Width: 80, Height: 24}); err != nil {
		t.Fatal(err)
	}
	wait, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	if _, err := output.Wait(wait, func(s tuiterm.Snapshot) bool { return s.AltScreen }); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := renderer.close(); err != nil {
		t.Fatal(err)
	}
	raw := string(output.Bytes())
	for _, mode := range []string{"1000", "1006"} {
		enabled, disabled := strings.Index(raw, "\x1b[?"+mode+"h"), strings.LastIndex(raw, "\x1b[?"+mode+"l")
		if enabled < 0 || disabled <= enabled {
			t.Errorf("mode %s not restored on cancellation", mode)
		}
	}
	for _, mode := range []string{"1002", "1003"} {
		if strings.Contains(raw, "\x1b[?"+mode+"h") {
			t.Errorf("motion mode %s enabled", mode)
		}
	}
}
