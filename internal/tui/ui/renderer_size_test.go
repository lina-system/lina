// SPDX-License-Identifier: Apache-2.0

package ui

import (
	tea "charm.land/bubbletea/v2"
	"testing"
)

func TestRendererRejectsLateStartupSize(t *testing.T) {
	renderer := &terminalRenderer{}
	wanted := tea.WindowSizeMsg{Width: 120, Height: 60}
	renderer.expectedSize.Store(&wanted)
	if renderer.filterSize(nil, tea.WindowSizeMsg{Width: 80, Height: 24}) != nil {
		t.Fatal("late startup size overwrote the conversation's terminal size")
	}
	if got := renderer.filterSize(nil, wanted); got != wanted {
		t.Fatal("authoritative size was discarded")
	}
	if _, ok := renderer.filterSize(nil, tea.QuitMsg{}).(tea.QuitMsg); !ok {
		t.Fatal("size filter swallowed lifecycle input")
	}
}
func TestRendererExplicitResizeChangesAcceptedSize(t *testing.T) {
	renderer := &terminalRenderer{}
	previous := tea.WindowSizeMsg{Width: 80, Height: 24}
	renderer.expectedSize.Store(&previous)
	wanted := tea.WindowSizeMsg{Width: 40, Height: 12}
	renderer.expectedSize.Store(&wanted)
	if renderer.filterSize(nil, previous) != nil || renderer.filterSize(nil, wanted) != wanted {
		t.Fatal("resize did not supersede the prior dimensions")
	}
}
