// SPDX-License-Identifier: Apache-2.0
package progress

import "sort"

// Line is display data only. Identity and navigation belong to Entry, never to
// the line. A nil percentage is unobserved, including an empty denominator.
type Line struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Stage        string `json:"stage"`
	Percent      *int   `json:"percent"`
	JustFinished string `json:"just_finished"`
	Next         string `json:"next"`
	Waiting      int    `json:"waiting"`
	Depth        int    `json:"depth"`
}
type Entry struct {
	Ref  string
	Line Line
}

// Unit contains observed work and source ownership, never a guessed plan.
// Done is used only to retire a seen completion; it cannot supply a plan stage.
// Landed allows acknowledgement-based retirement without a success verdict.
type Unit struct {
	ID, Parent, Kind, Name string
	Done                   *bool
	Active, Seen, Landed   bool
	JustFinished           string
	Waiting                int
	Updated                int64
}

func Compute(units []Unit) []Entry {
	type row struct {
		unit    Unit
		line    Line
		visible bool
	}
	rows := make(map[string]*row, len(units))
	for _, u := range units {
		line := Line{Name: u.Name, Kind: u.Kind, Stage: "unknown", JustFinished: "unknown", Next: "unknown", Waiting: u.Waiting}
		if u.JustFinished != "" {
			line.JustFinished = u.JustFinished
		}
		landed := u.Landed || u.Done != nil && *u.Done
		rows[u.ID] = &row{u, line, (u.Active || u.Waiting > 0 || landed && !u.Seen) && !(landed && u.Seen)}
	}
	// A goal is visible only through live children or a pending decision. Plan
	// stage and denominators remain unknown until the to-do record work lands.
	for _, u := range units {
		if u.Kind != "goal" {
			continue
		}
		r := rows[u.ID]

		r.visible = u.Waiting > 0
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
