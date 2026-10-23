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

func TestRendererRetainsEmojiWiderThanCodepointBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := tuiterm.NewRecorder(40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	renderer := startTerminalRenderer(ctx, rawTerminalWriter{output}, 40, 10, TrueColor)
	defer renderer.close()
	view := tea.NewView("❤️ tail\n👩‍💻 ❤️ tail")
	view.AltScreen = true
	if err := renderer.showFlushed(view, tea.WindowSizeMsg{Width: 40, Height: 10}); err != nil {
		t.Fatal(err)
	}
	for y, row := range output.Snapshot().Cells[:2] {
		var text strings.Builder
		for _, cell := range row {
			text.WriteString(cell.Content)
		}
		if !strings.Contains(text.String(), "❤️") || !strings.Contains(text.String(), "tail") {
			t.Fatalf("row %d lost wider emoji: %q", y, text.String())
		}
	}
}
