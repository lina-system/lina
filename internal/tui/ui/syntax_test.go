// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/tui/render"
)

func syntaxRows(source string) []render.StyledRow {
	s := render.NewStream("syntax", render.Config{Width: 80}, nil)
	s.Seal(1, source)
	return s.Markdown(render.Config{Width: 80}, ">", parseMarkdown)
}

func paintSyntax(styles Styles, rows []render.StyledRow) string {
	lines := make([]string, len(rows))
	for i, row := range rows {
		lines[i] = styles.markdownRow(row)
	}
	return strings.Join(lines, "\n")
}

func TestFencedSyntaxGolden(t *testing.T) {
	samples := map[string]string{
		"go":                   "// comment\nfunc main() { fmt.Println(\"한글\", 42) }",
		"python":               "# comment\ndef greet(name):\n    return \"hello\", 42",
		"sh":                   "# comment\nif true; then echo \"hello\"; fi",
		"json":                 "{\"name\": \"한글\", \"count\": 42, \"ok\": true}",
		"unknown-language-456": "func main() { return 42 }",
	}
	for _, theme := range []string{"dark", "light", "high-contrast"} {
		t.Run(theme, func(t *testing.T) {
			styles := NewStyles(Options{Theme: theme, Profile: TrueColor})
			golden := map[string]string{}
			for lang, source := range samples {
				rows := syntaxRows("```" + lang + "\n" + source + "\n```")
				got := paintSyntax(styles, rows)
				if ansi.Strip(got) != source {
					t.Fatalf("%s changed code text: %q", lang, got)
				}
				highlighted := false
				for _, row := range rows {
					for _, span := range row.Spans {
						if styles.markdownRow(render.StyledRow{Spans: []render.Span{span}}) != styles.Code.Render(span.Text) {
							highlighted = true
						}
					}
				}
				wantHighlight := theme != "high-contrast" && lang != "unknown-language-456"
				if highlighted != wantHighlight {
					t.Errorf("%s: syntax highlight=%t, want %t", lang, highlighted, wantHighlight)
				}
				golden[lang] = got
			}
			if t.Failed() {
				return
			}
			data, err := json.MarshalIndent(golden, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, '\n')
			path := filepath.Join("testdata", "syntax-"+theme+".json")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(path, data, 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != string(want) {
				t.Errorf("syntax golden differs: %s", data)
			}
		})
	}
}

func TestFencedSyntaxStreamingAndFallback(t *testing.T) {
	styles := NewStyles(Options{Theme: "dark", Profile: TrueColor})
	config := render.Config{Width: 80}
	source := "```go\nvar answer = 42\n```\n"
	for split := range len(source) + 1 {
		s := render.NewStream("code", config, nil)
		s.Append(1, source[:split])
		rows := s.Markdown(config, ">", parseMarkdown)
		if split < len(source) {
			for _, row := range rows {
				for _, span := range row.Spans {
					if span.Style != (render.InlineStyle{}) {
						t.Fatalf("split %d styled open tail: %+v", split, span)
					}
				}
			}
		}
		s.Append(2, source[split:])
		got := paintSyntax(styles, s.Markdown(config, ">", parseMarkdown))
		if got == styles.Code.Render("var answer = 42") {
			t.Fatalf("split %d: closed fence not highlighted", split)
		}
	}
	for _, source := range []string{"```go\nvar answer = 42", "````go\nvar answer = 42\n```", "~~~go\nvar answer = 42\n```", "```\nvar answer = 42\n```", "    var answer = 42"} {
		rows := syntaxRows(source)
		for _, row := range rows {
			var line strings.Builder
			for _, span := range row.Spans {
				line.WriteString(span.Text)
			}
			if got := styles.markdownRow(row); got != styles.Code.Render(line.String()) {
				t.Errorf("fallback highlighted %q: %q", source, got)
			}
		}
	}
	opts := environmentOptions(func(key string) string {
		if key == "NO_COLOR" {
			return "1"
		}
		return ""
	})
	for _, profile := range []Profile{opts.Profile, ANSI16, ANSI256, TrueColor} {
		got := paintSyntax(NewStyles(Options{Profile: profile}), syntaxRows(source))
		if profile == Monochrome && strings.Contains(got, "\x1b") {
			t.Fatal("NO_COLOR emitted escapes")
		}
		if profile == ANSI16 && (strings.Contains(got, "38;2") || strings.Contains(got, "38;5")) {
			t.Fatal("16 color overflow")
		}
		if profile == ANSI256 && strings.Contains(got, "38;2") {
			t.Fatal("256 color overflow")
		}
	}
}

func TestFencedSyntaxCache(t *testing.T) {
	config := render.Config{Width: 80, Theme: "dark"}
	s := render.NewStream("cache", config, nil)
	calls := 0
	parse := func(source, bullet string) []render.MarkdownBlock { calls++; return parseMarkdown(source, bullet) }
	s.Append(1, "```go\nvar answer = 42\n```\n")
	first := s.Markdown(config, ">", parse)
	prefix := &first[0].Spans[0]
	s.Append(2, "open")
	second := s.Markdown(config, ">", parse)
	if prefix != &second[0].Spans[0] || calls != 1 {
		t.Fatal("append reparsed closed code")
	}
	for _, width := range []int{8, 80} {
		config.Width = width
		config.Theme = "light"
		s.Markdown(config, ">", parse)
	}
	if calls != 1 {
		t.Fatal("resize or theme re-lexed code")
	}
	s.Seal(3, "```python\nreturn 7\n```")
	got := styledText(s.Markdown(config, ">", parse))
	if calls != 2 || got != "return 7" {
		t.Fatal("revision did not replace highlighted cache")
	}
}

func TestFencedSyntaxClosureAndWidth(t *testing.T) {
	for _, source := range []string{
		"~~~go\nvar answer = 42\n~~~~",
		"````go extra-info\nvar answer = 42\n`````",
		"> ```go\n> var answer = 42\n> ```",
		"- example\n\n  ```go\n  var answer = 42\n  ```",
	} {
		highlighted := false
		for _, row := range syntaxRows(source) {
			for _, span := range row.Spans {
				if span.Style.Syntax == render.SyntaxAccent {
					highlighted = true
				}
			}
		}
		if !highlighted {
			t.Errorf("matching fence did not highlight: %q", source)
		}
	}
	source := "```go\nvar 한글 = \"👩‍💻é\"\n```\n"
	s := render.NewStream("bytes", render.Config{Width: 80}, nil)
	for i := range len(source) {
		s.Append(uint64(i+1), source[i:i+1])
		rows := s.Markdown(render.Config{Width: 80}, ">", parseMarkdown)
		if i < len(source)-1 {
			for _, row := range rows {
				for _, span := range row.Spans {
					if span.Style != (render.InlineStyle{}) {
						t.Fatal("byte stream styled open tail")
					}
				}
			}
		}
	}
	for _, mode := range []render.WidthMode{render.WCWidth, render.Graphemes} {
		for _, width := range []int{1, 5, 12, 80} {
			rows := s.Markdown(render.Config{Width: width, Mode: mode}, ">", parseMarkdown)
			for _, row := range rows {
				line := styledText([]render.StyledRow{row})
				if (render.Unicode{WidthMode: mode}).Width(line) > width {
					t.Fatalf("width %d: %q", width, line)
				}
				if strings.Count(line, "👩") != strings.Count(line, "👩‍💻") || strings.Count(line, "e") != strings.Count(line, "é") {
					t.Fatalf("split cluster: %q", line)
				}
			}
		}
	}
}
