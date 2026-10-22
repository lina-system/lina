// SPDX-License-Identifier: Apache-2.0
package result

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Measurer supplies display-cell width for table alignment. Nil counts Unicode
// scalar values. It must be deterministic and must not alter the body.
type Measurer interface{ Width(string) int }

// PlainText renders validated bodies completely, without evaluation or fetching.
// Ordering comes from the body, never from maps or the environment.
func PlainText(b Body, m Measurer) string {
	var out []string
	add := func(s string) { out = append(out, s) }
	list := func(label string, items []string) {
		add(label + ":")
		for _, s := range items {
			add("- " + s)
		}
	}
	switch v := b.(type) {
	case *Summary:
		for _, s := range v.Outline {
			add(s.Heading)
			if s.Notes != "" {
				add(s.Notes)
			}
		}
		list("Key points", v.KeyPoints)
		list("Decisions", v.Decisions)
		add("Open questions:")
		for _, q := range v.Questions {
			add(q.Question)
			add("Answer: " + q.Answer)
		}
		list("Includes", v.Scope.Includes)
		list("Excludes", v.Scope.Excludes)
		add(fmt.Sprintf("Length: %g screens", v.Length))
	case *Table:
		return tableText(v, m)
	case *Diagram:
		add(v.Kind + " (" + v.Direction + ")")
		for _, n := range v.Nodes {
			add(n.ID + ": " + n.Label)
		}
		for _, e := range v.Edges {
			add(e.From + " -> " + e.To + ": " + e.Label)
		}
		for _, g := range v.Groups {
			add(g.Label + ": " + strings.Join(g.Nodes, ", "))
		}
	case *Chart:
		add(v.Kind)
		add("X: " + axisText(v.X))
		add("Y: " + axisText(v.Y))
		for _, s := range v.Series {
			add(s.Name + ":")
			for _, p := range s.Values {
				add(cellText(p.X) + ": " + strconv.FormatFloat(p.Y, 'g', -1, 64))
			}
		}
	case *Draft:
		if v.FromSummary != nil {
			add(fmt.Sprintf("From summary: %s revision %d", v.FromSummary.ID, v.FromSummary.Revision))
		}
		for _, b := range v.Blocks {
			switch b.Kind {
			case "heading":
				add(strings.Repeat("#", b.Level) + " " + b.Text)
			case "paragraph", "quote":
				add(b.Text)
			case "list":
				for _, s := range b.Items {
					add("- " + s)
				}
			case "table":
				add(tableText(b.Table, m))
			case "code":
				add("Code (" + b.Language + "):")
				add(b.Text)
			case "image":
				add(fmt.Sprintf("Image: %s (%s revision %d)", b.Text, b.Asset.ID, b.Asset.Revision))
			case "citation":
				a := b.Anchor
				source := a.EventID
				if a.Asset != nil {
					source = fmt.Sprintf("%s revision %d", a.Asset.ID, a.Asset.Revision)
				}
				add(fmt.Sprintf("%s [%s, %d:%d]", b.Text, source, a.Start, a.End))
			}
		}
	case *Comparison:
		if v.Apply != nil {
			add("Apply: " + *v.Apply)
		}
		for _, t := range v.Targets {
			add(t.Label)
			add("Old:")
			add(t.Old)
			add("New:")
			add(t.New)
			for _, h := range t.Hunks {
				add(fmt.Sprintf("Hunk: -%d,%d +%d,%d", h.OldStart, h.OldCount, h.NewStart, h.NewCount))
			}
		}
	case *Plan:
		if v.Confirm != nil {
			add("Confirm: " + *v.Confirm)
		}
		for _, e := range v.Entries {
			line := fmt.Sprintf("%s: %s (%s, %s)", e.ID, e.Title, e.Kind, e.State)
			if e.Parent != nil {
				line += "; parent: " + *e.Parent
			}
			if e.Done != "" {
				line += "; done: " + e.Done
			}
			if e.Stage != "" {
				line += "; stage: " + e.Stage
			}
			if len(e.DependsOn) > 0 {
				line += "; depends on: " + strings.Join(e.DependsOn, ", ")
			}
			add(line)
		}
	case *File:
		add(fmt.Sprintf("%s (%s, %d bytes)\nAsset: %s revision %d", v.Name, v.MediaType, v.Size, v.Asset.ID, v.Asset.Revision))
	}
	return strings.Join(out, "\n")
}
func axisText(a Axis) string {
	if a.Unit != "" {
		return a.Label + " (" + a.Unit + ")"
	}
	return a.Label
}
func cellText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
func tableText(t *Table, m Measurer) string {
	width := utf8.RuneCountInString
	if m != nil {
		width = m.Width
	}
	rows := [][]string{make([]string, len(t.Columns))}
	sizes := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		rows[0][i] = c.Name + " [" + c.Type + "]"
		if c.Unit != "" {
			rows[0][i] += " (" + c.Unit + ")"
		}
	}
	for _, row := range t.Rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = cellText(c)
		}
		rows = append(rows, cells)
	}
	if t.Totals != nil {
		cells := make([]string, len(t.Totals))
		for i, c := range t.Totals {
			cells[i] = cellText(c)
		}
		rows = append(rows, cells)
	}
	for _, row := range rows {
		for i, c := range row {
			sizes[i] = max(sizes[i], width(c))
		}
	}
	lines := make([]string, len(rows))
	for n, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = c + strings.Repeat(" ", min(1000, max(0, sizes[i]-width(c))))
		}
		lines[n] = strings.Join(cells, " | ")
	}
	if t.Totals != nil {
		lines[len(lines)-1] = "Totals: " + lines[len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
