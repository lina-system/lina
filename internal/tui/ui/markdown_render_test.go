// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"github.com/thisisjun786/lina/internal/tui/render"
	"strings"
	"testing"
)

func styledText(rows []render.StyledRow) string {
	lines := make([]string, len(rows))
	for i, row := range rows {
		for _, span := range row.Spans {
			lines[i] += span.Text
		}
	}
	return strings.Join(lines, "\n")
}

func TestMarkdownStructureAndLiteralContent(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"**bold and *italic***", "bold and italic"},
		{"__bold__ and _italic_", "bold and italic"},
		{"- outer\n  - **inner**\n- next", "> outer\n  > inner\n> next"},
		{"2) two\n3) three", "2. two\n3. three"},
		{"`` `code` **literal** ``", "`code` **literal**"},
		{"~~~txt\n**code**\n\n[not a link](file:///tmp)\n~~~", "**code**\n\n[not a link](file:///tmp)"},
		{"    *code*\n    _literal_", "*code*\n_literal_"},
		{"[**bold** *italic*](https://example.com)", "bold italic"},
		{"[reference][doc]\n\n[doc]: https://example.com", "reference"},
		{"\\*literal\\* &amp; &#42; snake_case", "*literal* & * snake_case"},
		{"first\nsecond\n\nthird", "first\nsecond\n\nthird"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			s := render.NewStream("md", render.Config{Width: 80}, nil)
			s.Seal(1, tc.source)
			got := styledText(s.Markdown(render.Config{Width: 80}, ">", parseMarkdown))
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	s := render.NewStream("md", render.Config{Width: 80}, nil)
	s.Seal(1, "**bold *both***")
	rows := s.Markdown(render.Config{Width: 80}, ">", parseMarkdown)
	if !rows[0].Spans[0].Style.Bold || !rows[0].Spans[1].Style.Bold || !rows[0].Spans[1].Style.Italic {
		t.Fatalf("nested emphasis lost: %+v", rows)
	}
}

func TestMarkdownStreamingEverySplit(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"**굵게**", "굵게"},
		{"__굵게__", "굵게"},
		{"*기울임*", "기울임"},
		{"`code`", "code"},
		{"[문서](https://example.com)", "문서"},
		{"- 목록\n- 다음", "> 목록\n> 다음"},
		{"```go\n**literal**\n```\n", "**literal**"},
		{"~~~\ncode\n~~~\n", "code"},
		{"```go\ncode\n```  \n", "code"},
		{"    **literal**\n    `literal`", "**literal**\n`literal`"},
		{"[link](https://example.com/a\\)b) next", "link next"},
		{"**closed**\n\n*open*", "closed\n\nopen"},
		{"한글👩‍💻 **끝**", "한글👩‍💻 끝"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			for split := range len(tc.source) + 1 {
				s := render.NewStream("md", render.Config{Width: 80}, nil)
				s.Append(1, tc.source[:split])
				s.Markdown(render.Config{Width: 80}, ">", parseMarkdown)
				s.Append(2, tc.source[split:])
				got := strings.TrimRight(styledText(s.Markdown(render.Config{Width: 80}, ">", parseMarkdown)), "\n")
				if got != tc.want {
					t.Fatalf("split %d: got %q, want %q", split, got, tc.want)
				}
			}
		})
	}
}

func TestMarkdownDisplayWidthAndResize(t *testing.T) {
	source := "- **한글👩‍💻é 🇰🇷** [긴 링크](https://example.com) 끝\n\n```\n👩‍💻한글\n```"
	for _, mode := range []render.WidthMode{render.WCWidth, render.Graphemes} {
		s := render.NewStream("md", render.Config{Width: 80}, nil)
		s.Seal(1, source)
		for _, width := range []int{80, 12, 5, 1, 40, 80} {
			rows := s.Markdown(render.Config{Width: width, Mode: mode}, ">", parseMarkdown)
			for _, line := range strings.Split(styledText(rows), "\n") {
				if (render.Unicode{WidthMode: mode}).Width(line) > width {
					t.Fatalf("mode %d width %d: %q", mode, width, line)
				}
				if strings.Count(line, "👩") != strings.Count(line, "👩‍💻") || strings.Count(line, "🇰") != strings.Count(line, "🇰🇷") || strings.Contains(line, "e") && !strings.Contains(line, "é") {
					t.Fatalf("split cluster: %q", line)
				}
			}
			if width == 80 && !strings.Contains(styledText(rows), "끝") {
				t.Fatal("resize lost final content")
			}
		}
	}
	s := render.NewStream("md", render.Config{Width: 80}, nil)
	s.Append(1, "**"+strings.Repeat("한글 ", 30))
	s.Markdown(render.Config{Width: 80}, ">", parseMarkdown)
	got := styledText(s.Markdown(render.Config{Width: 12}, ">", parseMarkdown))
	if strings.Count(got, "한글") != 30 {
		t.Fatalf("open resize lost text: %q", got)
	}
	s.Append(2, "끝**")
	if !strings.HasSuffix(styledText(s.Markdown(render.Config{Width: 12}, ">", parseMarkdown)), "끝") {
		t.Fatal("append after resize lost text")
	}
}

func TestMarkdownCacheAndAuthoritativeSeal(t *testing.T) {
	config := render.Config{Width: 80}
	s := render.NewStream("md", config, nil)
	s.Append(1, "**closed**\n\n*open")
	first := s.Markdown(config, ">", parseMarkdown)
	prefix := &first[0].Spans[0]
	s.Append(2, " tail")
	second := s.Markdown(config, ">", parseMarkdown)
	if prefix != &second[0].Spans[0] {
		t.Fatal("append reflowed closed prefix")
	}
	s.Append(3, "*\n\nnext")
	third := s.Markdown(config, ">", parseMarkdown)
	if prefix != &third[0].Spans[0] {
		t.Fatal("new closure reflowed earlier prefix")
	}
	if &third[0] != &s.Markdown(config, ">", parseMarkdown)[0] {
		t.Fatal("unchanged view rebuilt rows")
	}
	s.Seal(4, "**authoritative**")
	if got := styledText(s.Markdown(config, ">", parseMarkdown)); got != "authoritative" {
		t.Fatalf("seal = %q", got)
	}
	s.Append(5, "late")
	if got := styledText(s.Markdown(config, ">", parseMarkdown)); got != "authoritative" {
		t.Fatal("late delta changed sealed content")
	}
	s.Seal(5, "*replacement*")
	if got := styledText(s.Markdown(config, ">", parseMarkdown)); got != "replacement" {
		t.Fatal("new authoritative revision ignored")
	}
}

func TestMarkdownLinkSchemesAndSanitization(t *testing.T) {
	for _, tc := range []struct {
		destination string
		allowed     bool
	}{
		{"https://example.com/a?x=1&amp;y=2", true}, {"http://example.com", true}, {"mailto:hello@example.com", true},
		{"javascript:alert(1)", false}, {"file:///tmp/test", false}, {"data:text/plain,test", false}, {"relative", false},
		{"https://example.com/&#10;bad", false},
	} {
		s := render.NewStream("md", render.Config{Width: 80}, nil)
		s.Seal(1, "[label]("+tc.destination+")")
		rows := s.Markdown(render.Config{Width: 80}, ">", parseMarkdown)
		if got := styledText(rows); got != "label" {
			t.Fatalf("%s: label = %q", tc.destination, got)
		}
		if got := rows[0].Spans[0].Style.URL != ""; got != tc.allowed {
			t.Fatalf("%s: allowed=%v", tc.destination, got)
		}
	}
	s := render.NewStream("md", render.Config{Width: 80}, nil)
	s.Seal(1, "**safe\x1b]52;c;bad\a&#27;[31m**")
	if strings.ContainsAny(styledText(s.Markdown(render.Config{Width: 80}, ">", parseMarkdown)), "\x1b\a") {
		t.Fatal("terminal escape survived decoding")
	}
}
