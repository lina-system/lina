// SPDX-License-Identifier: Apache-2.0
package state

import (
	"maps"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Board keeps Core's order within each of the three presentation groups.
// Selection and expansion follow identities through updates and regrouping.
type Board struct {
	Rows     []BoardRow
	Selected string
	Expanded map[string]bool
}
type BoardRow struct {
	ID, Text, Detail, RunID string
	Group                   int // 0: waiting for me, 1: running, 2: done
	RunState                protocol.RunningState
}

func (v Board) Replace(running []Block, waiting []protocol.WaitingRow) Board {
	groups := [3][]BoardRow{}
	seenRuns := map[string]bool{}
	for _, row := range waiting {
		text := row.Line.Sentence
		if row.Line.AgentName != "" {
			text = row.Line.AgentName + ": " + text
		}
		entry := BoardRow{ID: "waiting:" + row.Ref.Source + ":" + row.Ref.Key, Text: sanitize.Display(text), Detail: sanitize.Display(row.Line.Context)}
		if row.Line.Recommendation != "" {
			entry.Detail += "\n" + sanitize.Display(row.Line.Recommendation)
		}
		switch row.Ref.Source {
		case "run":
			entry.RunID = row.Ref.Key
		case "work":
			entry.RunID = "task:" + row.Ref.Key
		}
		if entry.RunID != "" {
			entry.ID = "run:" + entry.RunID
			seenRuns[entry.RunID] = true
			for _, run := range running {
				if run.RunID == entry.RunID {
					entry.RunState = run.RunState
					break
				}
			}
		}
		groups[0] = append(groups[0], entry)
	}
	for _, row := range running {
		if row.Removed || seenRuns[row.RunID] {
			continue
		}
		group := 1
		switch row.RunState {
		case protocol.RunningStateNeedsInput:
			group = 0
		case protocol.RunningStateWaiting:
			if row.RunReason == protocol.RunningReasonPerson {
				group = 0
			}
		case protocol.RunningStateReadyForReview, protocol.RunningStateDone, protocol.RunningStateFailed, protocol.RunningStateStopped:
			group = 2
		case protocol.RunningStateInProgress, protocol.RunningStateUnknown, "":
		}
		id := row.ID
		if row.RunID != "" {
			id = "run:" + row.RunID
			seenRuns[row.RunID] = true
		}
		groups[group] = append(groups[group], BoardRow{ID: id, Text: sanitize.Display(row.Text), RunID: row.RunID, RunState: row.RunState, Group: group})
	}
	out := Board{Selected: v.Selected, Expanded: map[string]bool{}}
	for _, group := range groups {
		out.Rows = append(out.Rows, group...)
	}
	for _, row := range out.Rows {
		out.Expanded[row.ID] = v.Expanded[row.ID]
	}
	if !slices.ContainsFunc(out.Rows, func(row BoardRow) bool { return row.ID == out.Selected }) {
		out.Selected = ""
		if len(out.Rows) > 0 {
			out.Selected = out.Rows[0].ID
		}
	}
	return out
}
func (v Board) Move(delta int) Board {
	if len(v.Rows) == 0 {
		return v
	}
	i := slices.IndexFunc(v.Rows, func(row BoardRow) bool { return row.ID == v.Selected })
	v.Selected = v.Rows[max(0, min(len(v.Rows)-1, i+delta))].ID
	return v
}
func (v Board) Toggle() Board {
	v.Expanded = maps.Clone(v.Expanded)
	if v.Expanded == nil {
		v.Expanded = map[string]bool{}
	}
	if v.Selected != "" {
		v.Expanded[v.Selected] = !v.Expanded[v.Selected]
	}
	return v
}
func (row BoardRow) CanStop() bool {
	return row.RunID != "" && row.Group != 2
}
func (row BoardRow) CanNote() bool {
	return row.CanStop() && !strings.HasPrefix(row.RunID, "task:")
}
