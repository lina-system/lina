// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/tui/render"
)

// Some terminals join emoji even without mode 2027. Bubble Tea reserves the
// default codepoint width but repaints a whole row without anchoring its fields.
// Repair only rows with such width disagreements, anchoring the next space to
// its intended column. Adjacent graphemes and ANSI styling remain together.
func alignedUnicodeRows(content string, width, height int) string {
	var repair strings.Builder
	for y, row := range strings.Split(content, "\n") {
		if y >= height {
			break
		}
		var line strings.Builder
		x := 0
		drift, pending, clipped := false, false, false
		wider := false
		var parserState byte
		for row != "" {
			seq, cells, n, next := ansi.DecodeSequence(row, parserState, nil)
			if n == 0 {
				break
			}
			row, parserState = row[n:], next
			if cells == 0 {
				line.WriteString(seq)
				continue
			}
			reserved := (render.Unicode{}).Width(seq)
			wider = wider || reserved < cells
			if clipped || x+reserved > width {
				clipped = true
				continue
			}
			if pending && seq == " " {
				line.WriteString(ansi.CursorPosition(x+1, y+1))
				pending = false
			}
			line.WriteString(seq)
			x += reserved
			if cells != reserved {
				drift, pending = true, true
			}
		}
		// A terminal can also draw a cluster wider than its reservation.
		// Seeking backward would erase that glyph; retain the normal renderer
		// for such a row until a wider layout is negotiated.
		if drift && !wider {
			repair.WriteString(ansi.CursorPosition(1, y+1))
			repair.WriteString(ansi.ResetStyle + ansi.EraseLineRight)
			repair.WriteString(line.String())
		}
	}
	if repair.Len() == 0 {
		return ""
	}
	return ansi.SaveCursor + ansi.ResetModeAutoWrap + repair.String() + ansi.RestoreCursor + ansi.SetModeAutoWrap
}
