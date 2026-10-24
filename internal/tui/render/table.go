// SPDX-License-Identifier: Apache-2.0

package render

import "strings"

type TableAlignment uint8

const (
	TableLeft TableAlignment = iota
	TableCenter
	TableRight
)

// MarkdownTable contains parsed single-line cells, including the header row.
// The UI owns parsing and styling; the table layout shares the text measurer.
type MarkdownTable struct {
	Cells      [][][]Span
	Alignments []TableAlignment
}

// Rows aligns columns at their natural display widths and clips at the right
// edge. Cells never wrap into adjacent columns. Retaining the semantic table
// lets a wider viewport reveal content again without reparsing its source.
func (t *MarkdownTable) Rows(width int, m Measurer) []StyledRow {
	if width <= 0 {
		return nil
	}
	widths := make([]int, len(t.Alignments))
	cellWidths := make([][]int, len(t.Cells))
	for i, row := range t.Cells {
		cellWidths[i] = make([]int, len(widths))
		for j, cell := range row {
			if j >= len(widths) {
				break
			}
			var text strings.Builder
			for _, span := range cell {
				text.WriteString(span.Text)
			}
			cellWidths[i][j] = m.Width(text.String())
			widths[j] = max(widths[j], cellWidths[i][j])
		}
	}
	rows := make([]StyledRow, 0, len(t.Cells))
	for i, cells := range t.Cells {
		spans := []Span{{Text: "|"}}
		used := 1
		for j, size := range widths {
			padding := size - cellWidths[i][j]
			left := 0
			switch t.Alignments[j] {
			case TableLeft:
			case TableCenter:
				left = padding / 2
			case TableRight:
				left = padding
			}
			// Bound padding by the viewport, even for very wide source cells.
			spans = append(spans, Span{Text: strings.Repeat(" ", min(width, left+1))})
			if j < len(cells) {
				spans = append(spans, cells[j]...)
			}
			spans = append(spans, Span{Text: strings.Repeat(" ", min(width, padding-left+1)) + "|"})
			used += size + 3
			if used > width {
				break
			}
		}
		// Clip the joined text once so clusters crossing style boundaries are
		// kept or omitted whole. Then restore the spans for the visible bytes.
		var text strings.Builder
		for _, span := range spans {
			text.WriteString(span.Text)
		}
		visible := len(Truncate(text.String(), width, m))
		row := StyledRow{Clipped: used > width}
		for _, span := range spans {
			if visible == 0 {
				break
			}
			n := min(visible, len(span.Text))
			row.Spans = append(row.Spans, Span{Text: span.Text[:n], Style: span.Style})
			visible -= n
		}
		rows = append(rows, row)
	}
	return rows
}
