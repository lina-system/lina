// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"sort"
	"time"
)

// Line is display data only. Identity and navigation belong to Entry, never to
// the line. A nil percentage is unobserved, including an empty denominator.
type Line struct {
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Now        string    `json:"now,omitempty"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Stage      string    `json:"stage"`
	State      string    `json:"state,omitempty"`
	// Paused is the person's pause on a goal line, as Core recorded it.
	Paused       bool    `json:"paused,omitempty"`
	Percent      *int    `json:"percent"`
	JustFinished string  `json:"just_finished"`
	Next         string  `json:"next"`
	Waiting      int     `json:"waiting"`
	Depth        int     `json:"depth"`
	Stages       []Stage `json:"stages,omitempty"`
}
type Entry struct {
	Ref  string
	Line Line
	// Row marks a row of a line: a task or run drawn inside an 일 or 목표. A row
	// is not a line and the running count never counts it.
	Row bool
}

// Unit contains observed work and source ownership, never a guessed plan.
// Done is used only to retire a seen completion; it cannot supply a plan stage.
// Landed allows acknowledgement-based retirement without a success verdict.
type Unit struct {
	StageGoal, StageAgent  string // Trusted dispatch origin, independent of its manager card.
	Agent                  string // Canonical owner; never part of the display line.
	StartedAt, FinishedAt  time.Time
	Now                    string
	ID, Parent, Kind, Name string
	State                  string // Optional observed execution state, independent of plan stage.
	// Paused is the person's pause on a goal line.
	Paused               bool
	Done                 *bool
	Active, Seen, Landed bool
	JustFinished         string
	Next                 string
	Waiting              int
	Updated              int64
	Percent              *int
	// GoalLine marks a line built from a 목표 record, whose Rank is the
	// person's order among goals.
	GoalLine bool
	// Rank is the person's order among goals. Zero is unranked, an older goal
	// admitted before ranks existed, so it sorts before a numbered rank.
	Rank   int
	Stages []Stage
}

func Compute(units []Unit) []Entry {
	type row struct {
		unit    Unit
		line    Line
		visible bool
		row     bool
	}
	// A step's card is a row of the 일 that runs it, not a line of its own. The
	// card carries its dispatch's id, which the step resolves from the goal's
	// worker actions, so one manager with twelve steps adds one line and twelve
	// rows.
	owner := map[string]string{}
	for _, u := range units {
		for _, stage := range u.Stages {
			for _, step := range stage.Steps {
				for _, card := range step.Cards {
					owner[card] = u.ID
				}
			}
		}
	}
	// A step's card belongs to its 일 through the step; a task card's own parent
	// link belongs to the 목표 or 일 it was handed under.
	type link struct {
		parent string
		step   bool
	}
	linkOf := func(u Unit) link {
		if u.Parent != "" {
			return link{parent: u.Parent}
		}
		if id := owner[u.ID]; id != "" {
			return link{parent: id, step: true}
		}
		return link{}
	}
	rows := make(map[string]*row, len(units))
	for _, u := range units {
		landed := u.Landed || u.Done != nil && *u.Done
		rows[u.ID] = &row{unit: u, line: UnitLine(u), visible: (u.Active || u.Waiting > 0 || landed && !u.Seen) && !(landed && u.Seen)}
	}
	// A 목표 is visible through its own work, live children or a pending decision.
	// Its children are 일 lines, so they stay lines and are counted; only the
	// rows of a 일 are not lines.
	for _, u := range units {
		if u.Kind != "goal" {
			continue
		}
		r := rows[u.ID]
		r.visible = r.visible || u.Waiting > 0
		for _, child := range units {
			if linkOf(child).parent != u.ID || child.Kind == "goal" {
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
		// The person's order decides between goal lines; every other row keeps
		// the movement order below.
		if a.unit.GoalLine && b.unit.GoalLine && a.unit.Rank != b.unit.Rank {
			return a.unit.Rank < b.unit.Rank
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
		at := linkOf(u)
		line := rows[at.parent]
		if at.parent == "" || at.parent == u.ID || line == nil || !line.visible {
			roots = append(roots, r)
			continue
		}
		// A step of an 일 is a row of that 일. A task handed under a 목표 is its
		// 일, a line of its own; a child of anything else is a row as well.
		r.row = at.step || line.unit.Kind != "goal"
		children[at.parent] = append(children[at.parent], r)
	}
	sort.Slice(roots, func(i, j int) bool { return less(roots[i], roots[j]) })
	out := []Entry{}
	for _, r := range roots {
		out = append(out, Entry{Ref: r.unit.ID, Line: r.line, Row: r.row})
		group := children[r.unit.ID]
		sort.Slice(group, func(i, j int) bool { return less(group[i], group[j]) })
		for _, child := range group {
			child.line.Depth = 1
			out = append(out, Entry{Ref: child.unit.ID, Line: child.line, Row: child.row})
		}
	}
	return out
}

// UnitLine is one unit's current projection before the running list drops a
// landed unit the person has already seen. Unobserved stage words stay unknown.
func UnitLine(u Unit) Line {
	line := Line{StartedAt: u.StartedAt, FinishedAt: u.FinishedAt, Now: u.Now, Name: u.Name, Kind: u.Kind, Stage: "unknown", State: u.State, Paused: u.Paused, Percent: u.Percent, JustFinished: "unknown", Next: "unknown", Waiting: u.Waiting, Stages: u.Stages}
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
	return line
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

// Count is the running count by line state, as the status line shows it.
type Count struct{ Running, Waiting int }

// CountLines counts the lines of a projection, never their rows: an 일, a task
// that is a goal of its own and a background run outside any 일 each count once
// however many workers they hold. A 목표 line is a heading over its 일 and is
// not counted. A line whose row waits on the person counts as waiting for them;
// the others count by their state word. The same ledger gives the same count,
// because it reads only the published lines.
func CountLines(entries []Entry) Count {
	count := Count{}
	for _, entry := range entries {
		line := entry.Line
		if entry.Row || line.Kind == "goal" {
			continue
		}
		switch {
		case line.Waiting > 0 || line.State == "needs_input":
			count.Waiting++
		case line.State == "in_progress":
			count.Running++
		}
	}
	return count
}
