// SPDX-License-Identifier: Apache-2.0

package render

import (
	"strings"
	"testing"
)

func TestTableClipsClustersAcrossStyles(t *testing.T) {
	table := MarkdownTable{
		Alignments: []TableAlignment{TableLeft, TableRight},
		Cells: [][][]Span{{
			{{Text: "👩", Style: InlineStyle{Bold: true}}, {Text: "‍💻"}},
			{{Text: "e"}, {Text: "́", Style: InlineStyle{Italic: true}}},
		}},
	}
	text := func(row StyledRow) string {
		var out strings.Builder
		for _, span := range row.Spans {
			out.WriteString(span.Text)
		}
		return out.String()
	}
	for _, mode := range []WidthMode{WCWidth, Graphemes} {
		m := Unicode{WidthMode: mode}
		whole := text(table.Rows(80, m)[0])
		for width := 1; width <= 20; width++ {
			rows := table.Rows(width, m)
			want := Truncate(whole, width, m)
			if got := text(rows[0]); got != want || rows[0].Clipped != (len(want) < len(whole)) {
				t.Fatalf("mode %d width %d split styled grapheme: %q, want %q", mode, width, got, want)
			}
		}
	}
}
