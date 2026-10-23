// SPDX-License-Identifier: Apache-2.0
package state

import (
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
	"slices"
)

// RecordList retains Core order. References are routing data; only the title,
// note and chips are display text. Selecting a row never changes its rank.
type RecordList struct {
	Rows     []protocol.RecordRow
	Selected int
}

func (v RecordList) Replace(rows []protocol.RecordRow) RecordList {
	selected := ""
	if v.Selected >= 0 && v.Selected < len(v.Rows) {
		selected = v.Rows[v.Selected].Ref
	}
	out := RecordList{Rows: slices.Clone(rows)}
	for i := range out.Rows {
		row := &out.Rows[i]
		row.Title = sanitize.Display(row.Title)
		row.Note = sanitize.Display(row.Note)
		row.Chips = slices.Clone(row.Chips)
		for j := range row.Chips {
			row.Chips[j].Value = sanitize.Display(row.Chips[j].Value)
		}
		if row.Ref == selected {
			out.Selected = i
		}
	}
	return out
}
func (v RecordList) Move(delta int) RecordList {
	v.Selected = max(0, min(len(v.Rows)-1, v.Selected+delta))
	return v
}
