// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"sort"
	"time"
)

// Line is display data only. Identity and navigation belong to Entry, never to
// the line. A nil percentage is unobserved, including an empty denominator.
type Line struct {
	StartedAt    time.Time `json:"started_at,omitzero"`
	FinishedAt   time.Time `json:"finished_at,omitzero"`
	Now          string    `json:"now,omitempty"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	Stage        string    `json:"stage"`
	State        string    `json:"state,omitempty"`
	Percent      *int      `json:"percent"`
	JustFinished string    `json:"just_finished"`
	Next         string    `json:"next"`
	Waiting      int       `json:"waiting"`
	Depth        int       `json:"depth"`
	Stages       []Stage   `json:"stages,omitempty"`
}
type Entry struct {
	Ref  string
	Line Line
}

// Unit contains observed work and source ownership, never a guessed plan.
// Done is used only to retire a seen completion; it cannot supply a plan stage.
// Landed allows acknowledgement-based retirement without a success verdict.
type Unit struct {
	StartedAt, FinishedAt  time.Time
	Now                    string
	ID, Parent, Kind, Name string
	State                  string // Optional observed execution state, independent of plan stage.
	Done                   *bool
	Active, Seen, Landed   bool
	JustFinished           string
	Next                   string
	Waiting                int
	Updated                int64
	Percent                *int
	Stages                 []Stage
}

func Compute(units []Unit) []Entry {
	parents := map[string]bool{}
	for _, u := range units {
		if u.Parent != "" {
			parents[u.Parent] = true
		}
	}
	type row struct {
		unit    Unit
		line    Line
		visible bool
	}
	rows := make(map[string]*row, len(units))
	for _, u := range units {
		line := Line{StartedAt: u.StartedAt, FinishedAt: u.FinishedAt, Now: u.Now, Name: u.Name, Kind: u.Kind, Stage: "unknown", State: u.State, Percent: u.Percent, JustFinished: "unknown", Next: "unknown", Waiting: u.Waiting, Stages: u.Stages}
		// An executable goal with its own graph and no separate project rows
		// is one piece of work. Grouping goals keep their children as the cards.
		if u.Kind == "goal" && !parents[u.ID] && len(u.Stages) > 0 {
			line.Kind = "project"
		}
		if u.JustFinished != "" {
			line.JustFinished = u.JustFinished
		}
		if u.Next != "" {
			line.Next = u.Next
		}
		// The line's stage word is its current stage entry: the lowest-numbered
		// entry that is not done.
		if current, ok := currentStage(u.Stages); ok {
			line.Stage = current.Label
		}
		landed := u.Landed || u.Done != nil && *u.Done
		rows[u.ID] = &row{u, line, (u.Active || u.Waiting > 0 || landed && !u.Seen) && !(landed && u.Seen)}
	}
	// A goal is visible through its own work, live children or a pending decision.
	for _, u := range units {
		if u.Kind != "goal" {
			continue
		}
		r := rows[u.ID]

		r.visible = r.visible || u.Waiting > 0
		for _, child := range units {
			if child.Parent != u.ID || child.Kind != "project" {
				continue
			}
			c := rows[child.ID]

			if c.visible {
				r.visible = true
				r.line.Waiting += c.line.Waiting

				if c.unit.Updated > r.unit.Updated {
					r.unit.Updated = c.unit.Updated
					r.line.JustFinished = c.line.JustFinished
					r.line.Next = c.line.Next
				}
			}
		}

	}
	less := func(a, b *row) bool {
		if (a.line.Waiting > 0) != (b.line.Waiting > 0) {
			return a.line.Waiting > 0
		}
		if a.unit.Updated != b.unit.Updated {
			return a.unit.Updated > b.unit.Updated
		}
		return a.unit.ID < b.unit.ID
	}
	roots := []*row{}
	children := map[string][]*row{}
	for _, u := range units {
		r := rows[u.ID]
		if !r.visible {
			continue
		}
		if parent := rows[u.Parent]; u.Kind == "project" && parent != nil && parent.visible && parent.unit.Kind == "goal" {
			children[u.Parent] = append(children[u.Parent], r)
		} else {
			roots = append(roots, r)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return less(roots[i], roots[j]) })
	out := []Entry{}
	for _, r := range roots {
		out = append(out, Entry{r.unit.ID, r.line})
		group := children[r.unit.ID]
		sort.Slice(group, func(i, j int) bool { return less(group[i], group[j]) })
		for _, child := range group {
			child.line.Depth = 1
			out = append(out, Entry{child.unit.ID, child.line})
		}
	}
	return out
}

// currentStage is the line's stage word: the lowest-numbered entry that is not
// done. A list always ends with the 확인 entry, so a started goal has one.
func currentStage(stages []Stage) (Stage, bool) {
	for _, entry := range stages {
		if entry.State != StepDone {
			return entry, true
		}
	}
	return Stage{}, false
}
