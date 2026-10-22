// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestStreamingCodePreview(t *testing.T) {
	for _, fence := range []string{"```go", "~~~~text"} {
		for _, chunk := range []int{1, 17, 100000} {
			t.Run(fmt.Sprintf("%s/chunk%d", fence, chunk), func(t *testing.T) {
				config := render.Config{Width: 80}
				s := render.NewStream("code", config, nil)
				prefix := "**settled**\n\nbefore\n"
				code := ""
				for i := 1; i <= 200; i++ {
					code += fmt.Sprintf("line%03d 한글 **literal**\n", i)
				}
				open := prefix + fence + "\n" + code
				var revision uint64
				for pos := 0; pos < len(open); pos += chunk {
					revision++
					s.Append(revision, open[pos:min(pos+chunk, len(open))])
					rows := s.Markdown(config, ">", parseMarkdown)
					text := styledText(rows)
					if strings.Contains(text, "writing code") && len(rows) != 10 {
						t.Fatalf("open preview rows=%d want 3 outside + 7 code", len(rows))
					}
				}
				rows := s.Markdown(config, ">", parseMarkdown)
				want := "settled\n\nbefore\nwriting code… (200 lines)\n" + strings.TrimSuffix(strings.Join(strings.Split(code, "\n")[194:200], "\n"), "\n")
				if got := styledText(rows); got != want {
					t.Fatalf("open fence has %d rows, want 10; got: %.250s", len(rows), got)
				}
				close := strings.Repeat(string(fence[0]), strings.IndexFunc(fence, func(r rune) bool { return r != rune(fence[0]) })) + "\n\nafter"
				for pos := 0; pos < len(close); pos += chunk {
					revision++
					s.Append(revision, close[pos:min(pos+chunk, len(close))])
					s.Markdown(config, ">", parseMarkdown)
				}
				whole := render.NewStream("whole", config, nil)
				whole.Append(1, open+close)
				final := s.Markdown(config, ">", parseMarkdown)
				if !reflect.DeepEqual(final, whole.Markdown(config, ">", parseMarkdown)) {
					t.Fatal("byte-split final rows differ from contiguous input")
				}
				text := styledText(final)
				if strings.Contains(text, "writing code") || strings.Count(text, "**literal**") != 200 || !strings.HasSuffix(text, "after") {
					t.Fatalf("closed fence did not restore full code: %d rows", len(final))
				}
			})
		}
	}
}

func TestStreamingCodePreviewBoundaryResizeAndSeal(t *testing.T) {
	config := render.Config{Width: 80}
	s := render.NewStream("code", config, nil)
	source := "**prefix**\n\n```\n" + strings.Repeat("abcdefghij\n", 8)
	s.Append(1, source)
	short := s.Markdown(config, ">", parseMarkdown)
	prefix := &short[0].Spans[0]
	if strings.Contains(styledText(short), "writing code") {
		t.Fatal("eight code rows were collapsed")
	}
	s.Append(2, "partial")
	rows := s.Markdown(config, ">", parseMarkdown)
	if len(rows) != 9 || styledText(rows[2:3]) != "writing code… (9 lines)" || !strings.HasSuffix(styledText(rows), "partial") {
		t.Fatalf("ninth partial row: %q", styledText(rows))
	}
	if prefix != &rows[0].Spans[0] {
		t.Fatal("preview rebuilt settled prefix")
	}
	if !rows[2].Spans[0].Style.Muted {
		t.Fatal("placeholder lost muted semantic style")
	}
	if &rows[0] != &s.Markdown(config, ">", parseMarkdown)[0] {
		t.Fatal("unchanged preview rebuilt rows")
	}
	for _, width := range []int{5, 1, 80} {
		resized := s.Markdown(render.Config{Width: width}, ">", parseMarkdown)
		muted := -1
		for i, row := range resized {
			for _, span := range row.Spans {
				if span.Style.Muted {
					muted = i
				}
			}
		}
		if muted < 0 || len(resized)-muted != 7 {
			t.Fatalf("width %d: preview not seven rows", width)
		}
		for _, line := range strings.Split(styledText(resized), "\n") {
			if (render.Unicode{}).Width(line) > width {
				t.Fatalf("width %d: overflow %q", width, line)
			}
		}
	}
	s.Append(3, "\n``\n~~~\n```not-a-closer\n")
	if !strings.Contains(styledText(s.Markdown(config, ">", parseMarkdown)), "writing code") {
		t.Fatal("mismatched or incomplete fence closed preview")
	}
	s.Seal(4, source+"partial\n```")
	sealed := s.Markdown(config, ">", parseMarkdown)
	if strings.Contains(styledText(sealed), "writing code") || strings.Count(styledText(sealed), "abcdefghij") != 8 {
		t.Fatal("Seal did not restore source")
	}
	s.Append(5, "late")
	if !reflect.DeepEqual(sealed, s.Markdown(config, ">", parseMarkdown)) {
		t.Fatal("late delta changed sealed rows")
	}
}

func TestStreamingCodePreviewLeavesProseUnchanged(t *testing.T) {
	config := render.Config{Width: 40}
	s := render.NewStream("prose", config, nil)
	source := strings.Repeat("plain text\n", 200)
	s.Append(1, source)
	if got := styledText(s.Markdown(config, ">", parseMarkdown)); got != source {
		t.Fatal("long prose was changed")
	}
}

func TestStreamingCodePreviewUsesMutedTheme(t *testing.T) {
	config := render.Config{Width: 80}
	s := render.NewStream("code", config, nil)
	s.Append(1, "```\n"+strings.Repeat("line\n", 9))
	row := s.Markdown(config, ">", parseMarkdown)[0]
	for _, profile := range []Profile{Monochrome, ANSI16, ANSI256, TrueColor} {
		styles := NewStyles(Options{Profile: profile, Width: 80})
		text := styledText([]render.StyledRow{row})
		if got := styles.markdownRow(row); got != styles.Muted.Render(text) {
			t.Fatalf("profile %d: placeholder not muted", profile)
		}
		if profile == Monochrome && strings.Contains(styles.markdownRow(row), "\x1b") {
			t.Fatal("monochrome emitted escapes")
		}
	}
}

func TestStreamingCodePreviewInResultDetails(t *testing.T) {
	for _, width := range []int{80, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := conversationShell()
			m.size.Width = width
			topic := "conversation:fixture"
			m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: topic}})
			m.Update(shellData{Generation: 1, Message: state.Turn{Active: true}})
			body := "before *literal*\n\n```go\n" + strings.Repeat("code line\n", 200)
			update := func(rev uint64, text string) {
				m.Update(shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: topic, Cursor: rev, Block: state.Block{ID: "result", Kind: protocol.KindResult, Body: text, Text: "summary", Revision: rev, Done: true}}})
			}
			update(1, body)
			m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
			if got := m.View().Content; strings.Count(got, "code line") != 6 || !strings.Contains(got, "writing code… (200 lines)") {
				t.Fatalf("live Details not collapsed: %s", got)
			}
			if !strings.Contains(strings.Join(m.pagerLines(), "\n"), "before *literal*") {
				t.Fatal("Details prose changed")
			}
			update(2, body+"```\n\nafter")
			if got := strings.Join(m.pagerLines(), "\n"); strings.Count(got, "code line") != 200 || strings.Contains(got, "writing code") {
				t.Fatal("closing fence did not expand Details")
			}
			// An interrupted or completed reply may have no matching closing fence.
			update(3, body)
			m.Update(shellData{Generation: 1, Message: state.Turn{Active: false}})
			if got := strings.Join(m.pagerLines(), "\n"); strings.Count(got, "code line") != 200 || strings.Contains(got, "writing code") {
				t.Fatal("turn completion did not reveal unclosed source")
			}
			m.Update(shellData{Generation: 1, Message: state.Turn{Active: true}})
			if got := strings.Join(m.pagerLines(), "\n"); strings.Contains(got, "writing code") {
				t.Fatal("next turn collapsed an earlier result")
			}
		})
	}
}
