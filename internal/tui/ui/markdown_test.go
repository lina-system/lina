// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
)

func markdownTranscript(m *shellModel) string {
	content := strings.Split(m.View().Content, "\n")
	region := m.conversationPlan().Conversation
	return strings.TrimRight(strings.Join(content[region.Y:region.Y+region.Height], "\n"), "\n")
}

func TestConversationMarkdownElements(t *testing.T) {
	cases := []struct{ name, source, plain string }{
		{"bold", "**굵게 bold**", "굵게 bold"},
		{"italic", "*기울임 italic*", "기울임 italic"},
		{"inline-code", "`한글_code()`", "한글_code()"},
		{"list", "- 첫 항목\n- 둘째", "› 첫 항목\n› 둘째"},
		{"ordered-list", "3. 셋째\n4. 넷째", "3. 셋째\n4. 넷째"},
		{"code-block", "```go\nfmt.Println(\"한글 **literal**\")\n```", "fmt.Println(\"한글 **literal**\")"},
		{"link", "[문서](https://example.com/docs)", "문서"},
	}
	for _, theme := range []string{"dark", "light", "high-contrast"} {
		golden := map[string]string{}
		for _, tc := range cases {
			t.Run(theme+"/"+tc.name, func(t *testing.T) {
				m := conversationShell()
				m.styles = NewStyles(Options{Theme: theme, Profile: TrueColor, Width: 80})
				m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Items: []state.Block{{ID: "md", Revision: 1, Text: tc.source, Done: true}}}})
				got := markdownTranscript(m)
				if plain := ansi.Strip(got); plain != tc.plain {
					t.Fatalf("rendered text = %q, want %q", plain, tc.plain)
				}
				golden[tc.name] = got
			})
		}
		if t.Failed() {
			continue
		}
		data, err := json.MarshalIndent(golden, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, '\n')
		path := filepath.Join("testdata", "markdown-"+theme+".json")
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(want) {
			t.Errorf("%s render differs from golden\ngot: %s", theme, data)
		}
	}
}

func TestConversationMarkdownLiteralHTML(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"block", "<div>hello</div>", "<div>hello</div>"},
		{"mixed", "**before**\n\n<div>hello</div>\n\n*after*", "before\n\n<div>hello</div>\n\nafter"},
		{"inline", "before <span title=\"&amp; **literal**\">hello</span> after", "before <span title=\"&amp; **literal**\">hello</span> after"},
		{"multiline", "<script>\n**literal** &amp;\n</script>", "<script>\n**literal** &amp;\n</script>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := conversationShell()
			m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Items: []state.Block{{ID: "html", Revision: 1, Text: tc.source, Done: true}}}})
			if got := ansi.Strip(markdownTranscript(m)); got != tc.want {
				t.Fatalf("rendered text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConversationMarkdownStreamingMarkers(t *testing.T) {
	for _, source := range []string{"**굵게**", "__굵게__", "_기울임_", "*기울임*", "`code`", "[문서](https://example.com)", "- 항목\n", "```go\ncode\n```\n"} {
		t.Run(source, func(t *testing.T) {
			m := conversationShell()
			m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture"}})
			for i := range len(source) {
				m.Update(shellData{Generation: 1, Message: state.Delta{Epoch: "1", Topic: "conversation:fixture", ID: "reply", Cursor: uint64(i + 1), Revision: uint64(i + 1), Text: source[i : i+1]}})
				got := ansi.Strip(markdownTranscript(m))
				if strings.ContainsAny(got, "*`[]") || strings.Contains(got, "https:") || strings.Contains(got, "- ") {
					t.Fatalf("prefix %q exposed markup: %q", source[:i+1], got)
				}
			}
		})
	}
}

func TestConversationMarkdownWrapsVisibleText(t *testing.T) {
	m := conversationShell()
	m.size.Width = 40
	source := "**" + strings.Repeat("한글👩‍💻 ", 12) + "끝**"
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Items: []state.Block{{ID: "md", Revision: 1, Text: source, Done: true}}}})
	got := ansi.Strip(markdownTranscript(m))
	if strings.Contains(got, "**") || !strings.HasSuffix(got, "끝") {
		t.Fatalf("wrapped markdown = %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if (render.Unicode{}).Width(line) > 40 {
			t.Fatalf("row exceeds display width: %q", line)
		}
		if strings.Count(line, "👩") != strings.Count(line, "👩‍💻") {
			t.Fatalf("split grapheme: %q", line)
		}
	}
}

func TestConversationMarkdownProfilesAndUnchangedOutput(t *testing.T) {
	for _, profile := range []Profile{Monochrome, ANSI16, ANSI256, TrueColor} {
		m := conversationShell()
		m.styles = NewStyles(Options{Profile: profile, ASCII: true, Width: 80})
		m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Items: []state.Block{{ID: "md", Revision: 1, Text: "- **bold** *italic* `code` [link](https://example.com)", Done: true}}}})
		got := markdownTranscript(m)
		if plain := ansi.Strip(got); plain != "> bold italic code link" {
			t.Fatalf("profile %d: %q", profile, plain)
		}
		if profile == Monochrome && strings.Contains(got, "\x1b") {
			t.Fatal("NO_COLOR emitted escapes")
		}
		if profile == ANSI16 && (strings.Contains(got, "38;2") || strings.Contains(got, "38;5")) {
			t.Fatal("16-color profile exceeded")
		}
		if profile == ANSI256 && strings.Contains(got, "38;2") {
			t.Fatal("256-color profile exceeded")
		}
		var output bytes.Buffer
		d := &display{output: &output, interactive: true, styles: m.styles}
		if err := d.showShell(m); err != nil {
			t.Fatal(err)
		}
		output.Reset()
		if err := d.showShell(m); err != nil {
			t.Fatal(err)
		}
		if output.Len() != 0 {
			t.Fatal("unchanged markdown wrote terminal output")
		}
	}
}

func TestConversationMarkdownRecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	m := conversationShell()
	m.styles = NewStyles(Options{Theme: "dark", Profile: TrueColor, Width: 80})
	h, err := term.Start(ctx, m, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.Send(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture"}})
	waitText := func(want string) {
		t.Helper()
		snapshot, err := h.Output.Wait(ctx, func(snapshot term.Snapshot) bool {
			var content strings.Builder
			for _, row := range snapshot.Cells {
				for _, cell := range row {
					content.WriteString(cell.Content)
				}
			}
			return strings.Contains(content.String(), want)
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range snapshot.Cells {
			for _, cell := range row {
				if cell.Content == "*" || cell.Content == "`" {
					t.Fatalf("raw marker on screen: %+v", cell)
				}
			}
		}
	}
	h.Send(shellData{Generation: 1, Message: state.Delta{Epoch: "1", Topic: "conversation:fixture", ID: "md", Revision: 1, Cursor: 1, Text: "**열린 강조"}})
	waitText("열린 강조")
	h.Send(shellData{Generation: 1, Message: state.Delta{Epoch: "1", Topic: "conversation:fixture", ID: "md", Revision: 2, Cursor: 2, Text: "**\n\n- 목록 항목"}})
	waitText("목록 항목")
	h.Send(shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: "conversation:fixture", Cursor: 3, Block: state.Block{ID: "md", Revision: 3, Text: "**완료** *기울임* `코드` [문서](https://example.com)\n\n- 목록 항목"}}})
	waitText("완료 기울임 코드 문서")
	h.Send(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	if snapshot := h.Output.Snapshot(); snapshot.AltScreen || snapshot.CursorHidden {
		t.Fatal("terminal modes not restored")
	}
}
