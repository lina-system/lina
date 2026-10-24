// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/tui/render"
)

const mixedTable = "| 항목 | 상태 | 메모 |\n|---|---|---|\n| 한글 | ready | 👩‍💻 |\n| Latin | 완료 | é 🇰🇷 |"

func TestMarkdownTableDisplayCells(t *testing.T) {
	for _, mode := range []render.WidthMode{render.WCWidth, render.Graphemes} {
		for _, width := range []int{40, 80, 140} {
			t.Run(fmt.Sprintf("%d/%d", mode, width), func(t *testing.T) {
				config := render.Config{Width: width, Mode: mode}
				s := render.NewStream("table", config, nil)
				s.Seal(1, mixedTable)
				rows := s.Markdown(config, ">", parseMarkdown)
				if len(rows) != 3 {
					t.Fatalf("table rows = %d, want 3: %s", len(rows), styledText(rows))
				}
				m := render.Unicode{WidthMode: mode}
				var boundaries []int
				for _, line := range strings.Split(styledText(rows), "\n") {
					var columns []int
					for i, r := range line {
						if r == '|' {
							columns = append(columns, m.Width(line[:i]))
						}
					}
					if len(columns) != 4 || m.Width(line) > width {
						t.Fatalf("invalid table row: %q (%v)", line, columns)
					}
					if boundaries == nil {
						boundaries = columns
					} else if !reflect.DeepEqual(columns, boundaries) {
						t.Fatalf("boundaries = %v, want %v", columns, boundaries)
					}
				}
			})
		}
	}
}

func TestMarkdownTableClippingAndResize(t *testing.T) {
	for _, mode := range []render.WidthMode{render.WCWidth, render.Graphemes} {
		config := render.Config{Width: 140, Mode: mode}
		s := render.NewStream("table", config, nil)
		s.Seal(1, "| A | B |\n|---|---|\n| "+strings.Repeat("한글👩‍💻é", 12)+" | LAST |")
		full := s.Markdown(config, ">", parseMarkdown)
		if len(full) != 2 {
			t.Fatalf("table wrapped into %d rows", len(full))
		}
		for _, width := range []int{40, 80, 1, 4, 140} {
			config.Width = width
			rows := s.Markdown(config, ">", parseMarkdown)
			if len(rows) != 2 {
				t.Fatalf("width %d: table wrapped into %d rows", width, len(rows))
			}
			for i, row := range rows {
				whole := styledText(full[i : i+1])
				want := render.Truncate(whole, width, render.Unicode{WidthMode: mode})
				if got := styledText(rows[i : i+1]); got != want || row.Clipped != (len(want) < len(whole)) {
					t.Fatalf("width %d: clipped row = %q (%t), want %q", width, got, row.Clipped, want)
				}
			}
		}
	}
}

func TestMarkdownTableStreamingClosure(t *testing.T) {
	config := render.Config{Width: 80}
	for split := range len(mixedTable) + 1 {
		s := render.NewStream("table", config, nil)
		s.Append(1, mixedTable[:split])
		s.Markdown(config, ">", parseMarkdown)
		s.Append(2, mixedTable[split:]+"\n")
		open := s.Markdown(config, ">", parseMarkdown)
		if !strings.Contains(styledText(open), "|---|---|---|") {
			t.Fatalf("split %d: open table was laid out", split)
		}
		for _, row := range open {
			for _, span := range row.Spans {
				if span.Style != (render.InlineStyle{}) {
					t.Fatalf("split %d: open table was styled", split)
				}
			}
		}
		s.Append(3, "\nAfter")
		closed := s.Markdown(config, ">", parseMarkdown)
		if strings.Contains(styledText(closed), "|---") || len(closed) != 5 {
			t.Fatalf("split %d: closed table = %q", split, styledText(closed))
		}
		prefix := &closed[0].Spans[0]
		s.Append(4, " table")
		if next := s.Markdown(config, ">", parseMarkdown); &next[0].Spans[0] != prefix {
			t.Fatal("open tail update rebuilt closed table")
		}
		s.Seal(5, mixedTable)
		if rows := s.Markdown(config, ">", parseMarkdown); len(rows) != 3 {
			t.Fatalf("sealed table = %q", styledText(rows))
		}
	}
}

func TestMarkdownTableCellsAndAlignment(t *testing.T) {
	config := render.Config{Width: 80}
	source := "Left | Center | Right\n:--- | :---: | ---:\n**한글** | `a\\|b` | [go](https://example.com)\nx | *é* | 12345\nshort\nextra | a | b | ignored"
	s := render.NewStream("table", config, nil)
	s.Seal(1, source)
	rows := s.Markdown(config, ">", parseMarkdown)
	want := "| Left  | Center | Right |\n| 한글  |  a|b   |    go |\n| x     |   é    | 12345 |\n| short |        |       |\n| extra |   a    |     b |"
	if got := styledText(rows); got != want {
		t.Fatalf("table cells = %q, want %q", got, want)
	}
	if got := VisibleMarkdown(source, ">"); got != want {
		t.Fatalf("visible table text = %q", got)
	}
	styles := map[string]render.InlineStyle{}
	var code strings.Builder
	for _, row := range rows {
		for _, span := range row.Spans {
			styles[span.Text] = span.Style
			if span.Style.Code {
				code.WriteString(span.Text)
			}
		}
	}
	if !styles["Left"].Bold || !styles["한글"].Bold || code.String() != "a|b" || !styles["é"].Italic || styles["go"].URL != "https://example.com" {
		t.Fatalf("table lost inline styles: %+v", styles)
	}
}

func TestMarkdownTableLiteralAndSafeContent(t *testing.T) {
	for _, source := range []string{
		"| A | B |\n| nope | --- |\n| x | y |",
		"| A | B |\n| --- |\n| x | y |",
	} {
		if got := VisibleMarkdown(source, ">"); got != source {
			t.Fatalf("malformed table changed: %q", got)
		}
	}
	if got := VisibleMarkdown("```\n"+mixedTable+"\n```", ">"); got != mixedTable {
		t.Fatal("code fence was parsed as a table")
	}
	s := render.NewStream("table", render.Config{Width: 80}, nil)
	s.Seal(1, "| A | B |\n|---|---|\n| &#10;safe | [link](javascript:alert) &#27;[31m |")
	rows := s.Markdown(render.Config{Width: 80}, ">", parseMarkdown)
	if len(rows) != 2 || strings.ContainsAny(styledText(rows), "\x1b\a") {
		t.Fatalf("unsafe cell rows: %q", styledText(rows))
	}
	for _, row := range rows {
		for _, span := range row.Spans {
			if span.Style.URL != "" {
				t.Fatal("unsafe table link was enabled")
			}
		}
	}
}
