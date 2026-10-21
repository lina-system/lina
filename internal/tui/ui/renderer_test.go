// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"testing"
)

func TestRendererKeepsNewestDimensionsAfterDelayedStartupSize(t *testing.T) {
	// The connection can publish a frame for the measured PTY before Bubble
	// Tea's asynchronous initial WindowSizeMsg reaches its event loop.
	initial := tea.WindowSizeMsg{Width: 120, Height: 24}
	current := tea.WindowSizeMsg{Width: 120, Height: 60}
	model := renderedView{size: initial}
	frame := tea.NewView("header\nfooter")
	updated, cmd := model.Update(renderedFrame{view: frame, size: current})
	if cmd == nil || cmd() != current {
		t.Fatal("new frame did not resize renderer")
	}
	updated, cmd = updated.Update(initial)
	if cmd == nil || cmd() != current {
		t.Fatal("late startup size overwrote the measured frame dimensions")
	}
	if updated.View().Content != frame.Content {
		t.Fatal("resize discarded current frame")
	}
	_, cmd = updated.Update(current)
	if cmd != nil {
		t.Fatal("matching dimensions caused a resize loop")
	}
	// A later real resize remains authoritative, including shrinking.
	smaller := tea.WindowSizeMsg{Width: 80, Height: 20}
	updated, cmd = updated.Update(renderedFrame{view: frame, size: smaller})
	if cmd == nil || cmd() != smaller {
		t.Fatal("real resize was lost")
	}
	_, cmd = updated.Update(current)
	if cmd == nil || cmd() != smaller {
		t.Fatal("an older queued resize won over the latest frame")
	}
}
