// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/layout"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"testing"
)

func TestLongReplyReadyRequiresSettledViewport(t *testing.T) {
	plan := layout.Plan(layout.Size{Width: 100, Height: 34}, layout.Caps{SidePane: true}, layout.Shape{ComposerRows: 1})
	end := "synthetic-last-line"
	affordance := design.Text(design.English, "reply.open_pager")
	makeFrame := func(busy bool) tuiterm.Snapshot {
		s := tuiterm.Snapshot{Cells: make([][]tuiterm.Cell, 34), AltScreen: true, CursorY: plan.Composer.Y}
		put := func(y int, value string) { s.Cells[y] = []tuiterm.Cell{{Content: value, Width: len(value)}} }
		bottom := plan.Conversation.Y + plan.Conversation.Height - 1
		if busy {
			put(bottom-2, end)
			put(bottom-1, affordance)
			put(bottom, "synthetic-activity")
		} else {
			put(bottom-1, end)
			put(bottom, affordance)
		}
		return s
	}
	if longReplyReady(makeFrame(true), plan, end, affordance) {
		t.Fatal("activity frame was accepted as a settled conversation")
	}
	if !longReplyReady(makeFrame(false), plan, end, affordance) {
		t.Fatal("settled complete viewport was refused")
	}
	partial := makeFrame(false)
	partial.CursorHidden = true
	if longReplyReady(partial, plan, end, affordance) {
		t.Fatal("partial redraw was accepted")
	}
	partial = makeFrame(false)
	partial.CursorY = 0
	if longReplyReady(partial, plan, end, affordance) {
		t.Fatal("cursor has not returned to the composer")
	}
}
