// SPDX-License-Identifier: Apache-2.0
package state

import (
	"cmp"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
	"maps"
	"slices"
)

// WorkerLanes is client display state. Unseen session identities start folded;
// snapshots preserve selection and expansion by identity, never by row index.
type WorkerLanes struct {
	Rows     []protocol.WorkerLane
	Selected string
	Expanded map[string]bool
}

func (v WorkerLanes) Replace(rows []protocol.WorkerLane) WorkerLanes {
	out := WorkerLanes{Selected: v.Selected, Expanded: map[string]bool{}}
	seen := map[string]int{}
	for _, row := range rows {
		if row.LaneID == "" {
			continue
		}
		// Keep opaque identities intact. Only display fields are terminal text.
		row.AgentName = sanitize.Display(row.AgentName)
		row.Worker = sanitize.Display(row.Worker)
		row.Title = sanitize.Display(row.Title)
		row.ThreadID = sanitize.Display(row.ThreadID)
		row.TurnID = sanitize.Display(row.TurnID)
		row.NodeID = sanitize.Display(row.NodeID)
		row.Details = slices.Clone(row.Details)
		for i := range row.Details {
			row.Details[i] = sanitize.Display(row.Details[i])
		}
		if i, ok := seen[row.LaneID]; ok {
			out.Rows[i] = row
		} else {
			seen[row.LaneID] = len(out.Rows)
			out.Rows = append(out.Rows, row)
		}
		out.Expanded[row.LaneID] = v.Expanded[row.LaneID]
	}
	slices.SortStableFunc(out.Rows, func(a, b protocol.WorkerLane) int {
		if n := cmp.Compare(a.AgentID, b.AgentID); n != 0 {
			return n
		}
		return cmp.Compare(a.TaskID, b.TaskID)
	})
	if _, ok := seen[out.Selected]; !ok {
		out.Selected = ""
		if len(out.Rows) > 0 {
			out.Selected = out.Rows[0].LaneID
		}
	}
	return out
}
func (v WorkerLanes) Move(delta int) WorkerLanes {
	if len(v.Rows) == 0 {
		return v
	}
	i := slices.IndexFunc(v.Rows, func(r protocol.WorkerLane) bool { return r.LaneID == v.Selected })
	v.Selected = v.Rows[max(0, min(len(v.Rows)-1, i+delta))].LaneID
	return v
}
func (v WorkerLanes) Toggle() WorkerLanes {
	if v.Selected == "" {
		return v
	}
	v.Expanded = maps.Clone(v.Expanded)
	if v.Expanded == nil {
		v.Expanded = map[string]bool{}
	}
	v.Expanded[v.Selected] = !v.Expanded[v.Selected]
	return v
}
